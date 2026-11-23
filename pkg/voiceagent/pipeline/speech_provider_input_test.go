package pipeline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
)

// speechEndpoint is an OpenAI-compatible speech server that records the input
// of every synthesis request and answers each with a short burst of PCM.
type speechEndpoint struct {
	mu       sync.Mutex
	inputs   []string
	requests chan struct{}
}

func (s *speechEndpoint) Inputs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.inputs...)
}

// newSpeechEndpoint starts the server and the real openai-speech provider
// pointed at it.
func newSpeechEndpoint(t *testing.T) (*speechEndpoint, *openaispeech.Provider) {
	t.Helper()
	endpoint := &speechEndpoint{requests: make(chan struct{}, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"speech-test"}]}`)
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input          string `json:"input"`
			ResponseFormat string `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ResponseFormat != "pcm" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		endpoint.mu.Lock()
		endpoint.inputs = append(endpoint.inputs, req.Input)
		endpoint.mu.Unlock()
		select {
		case endpoint.requests <- struct{}{}:
		default:
		}
		// 100 ms of a non-silent ramp: trailing silence is trimmed, and a
		// body of nothing but silence is not audio.
		body := make([]byte, 2*2400)
		for i := range 2400 {
			binary.LittleEndian.PutUint16(body[2*i:], uint16(int16(i%2000-1000)))
		}
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	provider, cleanup, err := openaispeech.New(&config.Config{
		TTSProvider:         config.OpenAISpeechProviderName,
		OpenAISpeechRuntime: "external",
		OpenAISpeechURL:     srv.URL + "/v1",
		OpenAISpeechModel:   "speech-test",
		OpenAISpeechVoice:   "test-voice",
		ModelsDir:           t.TempDir(),
	})
	if err != nil {
		t.Fatalf("openaispeech.New: %v", err)
	}
	t.Cleanup(cleanup)
	return endpoint, provider
}

// pausingBrain streams a scripted reply. before, when set, runs ahead of every
// chunk after the first, so a case can hold the stream on a condition.
type pausingBrain struct {
	fakeBrain
	before func(ctx context.Context, chunk int)
}

func (b *pausingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		for i, chunk := range b.chunks {
			if i > 0 && b.before != nil {
				b.before(ctx, i)
			}
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case out <- chunk:
			}
		}
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// drainingPlayer plays a stream by reading it to the end. The provider sets
// the sample rate before it sends its request, so a player that stops at
// WaitReady can finish the turn before the request reaches the server.
type drainingPlayer struct{}

func (drainingPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if _, err := stream.WaitReady(ctx); err != nil {
		return nil, err
	}
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		close(started)
		for range stream.Frames() {
		}
		done <- audio.PlaybackResult{Err: stream.Err()}
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}
func (drainingPlayer) Stop()           {}
func (drainingPlayer) IsPlaying() bool { return false }
func (drainingPlayer) Close() error    { return nil }

// talkReply runs one talk-mode turn over the scripted reply and returns what
// the speech server was asked to say and the transcript the turn finished with.
// A zero timer keeps the product default.
func talkReply(t *testing.T, endpoint *speechEndpoint, provider *openaispeech.Provider, b *pausingBrain, idle, hold time.Duration) ([]string, string) {
	t.Helper()
	bus := events.NewBus()
	var final string
	events.Subscribe(bus, func(e events.ResponseReady) { final = e.Response })
	p := &Pipeline{STT: &fakeSTT{text: "hello"}, Brain: b, TTS: provider, Player: drainingPlayer{}, Events: bus, idleFlush: idle, openingHold: hold}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := p.RunTurn(ctx); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	return endpoint.Inputs(), final
}

// The talk path end to end, as far as the wire: a scripted reply streamed
// through segmentation, admission, grouping and synthesis, and the exact input
// strings the openai-speech provider POSTs for it today.
func TestTalkReplyReachesTheSpeechServerAsWritten(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"short beats streamed promptly are one request", []string{"Mm-hmm. Yep. Got it."}, []string{"Mm-hmm. Yep. Got it."}},
		{"a trailing ellipsis survives", []string{"Yep, bye..."}, []string{"Yep, bye..."}},
		{"an elongation and its ellipsis survive", []string{"Riiiiiight... okay, so that's the bit that broke."}, []string{"Riiiiiight... okay, so that's the bit that broke."}},
		// A sentence that is only a voiced filler is not admitted, even
		// inside a reply that also answers.
		{"a filler-only sentence is left out of a longer reply", []string{"Hmm. Ohhh, I see. Ha! Yeah, that would do it."}, []string{"Ohhh, I see. Ha! Yeah, that would do it."}},
		// The scripted brain does no recovery substitution, so this pins only
		// that the bare filler is never sent.
		{"a filler-only reply sends nothing", []string{"Hmm."}, nil},
		// Two groups. The worker starts the second synthesis once the first
		// has a sample rate, so both requests are in flight together and may
		// reach the server in either order; playback order is the gate's.
		{"a filler before the recovery line is not sent", []string{"Hmm.", " Hmm, I lost my train of thought for a second. What were you saying?"}, []string{"Hmm, I lost my train of thought for a second.", "What were you saying?"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, provider := newSpeechEndpoint(t)
			// Only grouping policy and end of stream may flush, so the
			// requests do not depend on how fast the machine is.
			inputs, final := talkReply(t, endpoint, provider, &pausingBrain{fakeBrain: fakeBrain{chunks: tc.chunks}}, time.Hour, time.Hour)
			if !equalStrings(slices.Sorted(slices.Values(inputs)), slices.Sorted(slices.Values(tc.want))) {
				t.Fatalf("speech requests = %q, want %q", inputs, tc.want)
			}
			if want := strings.Join(tc.want, " "); final != want {
				t.Fatalf("transcript = %q, want %q", final, want)
			}
		})
	}

	// The brain holds the stream after the first beat until that beat has been
	// sent, so the beat goes alone and the rest of the reply is its own group.
	// The idle timer is out of reach: with only the opening deadline able to
	// flush, nothing can split "Yep." from "Got it." however slowly they are
	// scheduled, and the wait for the first request has no margin to miss.
	t.Run("a pause after the first beat splits the reply", func(t *testing.T) {
		endpoint, provider := newSpeechEndpoint(t)
		b := &pausingBrain{fakeBrain: fakeBrain{chunks: []string{"Mm-hmm. ", "Yep. Got it."}}}
		b.before = func(ctx context.Context, _ int) {
			select {
			case <-endpoint.requests:
			case <-ctx.Done():
			}
		}
		inputs, final := talkReply(t, endpoint, provider, b, time.Hour, 0)
		if want := []string{"Mm-hmm.", "Yep. Got it."}; !equalStrings(inputs, want) {
			t.Fatalf("speech requests = %q, want %q", inputs, want)
		}
		if final != "Mm-hmm. Yep. Got it." {
			t.Fatalf("transcript = %q, want the whole reply", final)
		}
	})
}
