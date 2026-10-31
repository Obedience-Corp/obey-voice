package voiceagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/ollama/ollama/api"
)

// The sink receives real selected-provider PCM but cannot claim physical
// listening. Completion must remain held without an audible delivery receipt.
type nativeDeliverySink struct{ frames atomic.Int64 }

func (*nativeDeliverySink) SinkKind() string { return audio.SinkStreamClient }
func (s *nativeDeliverySink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	attempt := audio.PlaybackAttemptFromContext(ctx)
	go func() {
		_, err := stream.WaitReady(ctx)
		close(started)
		if err == nil {
			for frame := range stream.Frames() {
				attempt.Write(func() { s.frames.Add(int64(len(frame))) })
			}
			err = stream.Err()
		}
		done <- attempt.Result(audio.PlaybackResult{Err: err})
	}()
	return audio.NewPlayback(started, done), nil
}
func (*nativeDeliverySink) Stop()           {}
func (*nativeDeliverySink) IsPlaying() bool { return false }
func (*nativeDeliverySink) Close() error    { return nil }

func TestNativeSelectedProviderTypedLocalAndCompletion(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var active, maximum, asr, chat atomic.Int32
	var mu sync.Mutex
	var speech []string
	var completion api.ChatRequest
	resultID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"selected","digest":"%s"}]}`, strings.Repeat("a", 64))
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","audio","speech"],"voices":["female"]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"selected"}]}`)
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/v1/audio/transcriptions":
			asr.Add(1)
			t.Error("typed/local/completion inferred ASR")
			w.WriteHeader(500)
		case "/v1/audio/speech":
			var req struct{ Model, Input, Voice string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Model != "selected" || req.Voice != "female" {
				t.Error("speech route drift", req)
			}
			mu.Lock()
			speech = append(speech, req.Input)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0, 2, 0, 3, 0})
		case "/api/chat":
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Model != "selected" {
				t.Error("chat route drift", req.Model)
			}
			chat.Add(1)
			text := "15"
			if req.Stream != nil && *req.Stream {
				text = "Typed answer."
			}
			if len(req.Format) > 0 {
				mu.Lock()
				completion = req
				id := resultID
				mu.Unlock()
				envelope, _ := json.Marshal(brain.CompletionEnvelope{Updates: []brain.CompletionUpdate{{ResultID: id, Text: "Completed fixture work."}}})
				text = string(envelope)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "selected", "message": map[string]string{"role": "assistant", "content": text}, "done": true, "done_reason": "stop"})
		default:
			t.Error("unexpected selected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "selected", OllamaNumCtx: 32768, TTSProvider: config.OpenAISpeechProviderName, TTSFallbackProvider: "disabled", TTSLookahead: 1, OpenAISpeechRuntime: "external", OpenAISpeechExternal: true, OpenAISpeechURL: server.URL + "/v1", OpenAISpeechModel: "selected", OpenAISpeechVoice: "female", ModelsDir: t.TempDir(), WorkDir: t.TempDir(), VoiceWorkerProvider: "ollama", VoiceWorkerModel: "independent-worker", VoiceWorkerOllamaHost: "http://127.0.0.1:1", VoiceWorkerMaxConcurrent: 2, AgentName: "Native fixture"}
	before := cfg.Clone()
	media := chatmedia.NewAudioSession()
	defer media.Close()
	candidate, err := PrepareNativeCandidate(t.Context(), cfg, NativeCandidateOptions{Lifetime: t.Context(), Media: media, Cache: nativeaudio.NewSupportCache(), Controls: "conversation", Transcript: "off"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = candidate.Close() }()
	sink := &nativeDeliverySink{}
	agent, cleanup, err := candidate.NewAgent(t.Context(), Options{Events: events.NewBus(), TextOnly: true, Player: sink, Worker: &stubBrain{}}, func(context.Context, *NativeCandidate) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := agent.EnableConversationWork("native-delivery", nil, candidate.PreparedConfig()); err != nil {
		t.Fatal(err)
	}
	agent.AdmitInput = func(_ context.Context, input string, _ bool) (pipeline.ResponseAdmission, error) {
		if input == "local" {
			return pipeline.ResponseAdmission{LocalReply: "Local fixture acknowledgement."}, nil
		}
		return pipeline.ResponseAdmission{Request: input}, nil
	}
	if err := agent.SendText(t.Context(), "A typed native question"); err != nil {
		t.Fatal(err)
	}
	chatBeforeLocal := chat.Load()
	if err := agent.SendText(t.Context(), "local"); err != nil {
		t.Fatal(err)
	}
	if chat.Load() != chatBeforeLocal {
		t.Fatal("local reply invoked chat")
	}
	// Include a real admitted WAV in the relevant completion excerpt. The
	// private structured request must carry it without modifying canonical turns.
	ref, err := media.ImportSamples(t.Context(), []float32{.1, -.2, .3})
	if err != nil {
		t.Fatal(err)
	}
	original, err := media.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	history := append(agent.Brain.History(), brain.Turn{Role: "user", TypedInput: "Consider the recording", Content: "Consider the recording", Utterance: &utterance.Finalized{ID: "completion-input", CaptureEpoch: 3, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}})
	projection := agent.Brain.(brain.HistoryProjectionProvider)
	if err := projection.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	c := agent.WorkCoordinator()
	scope := agent.ConversationScope()
	key, err := c.Register(scope, work.Admission{Task: "fixture task", Provider: cfg.VoiceWorkerProvider, Model: cfg.VoiceWorkerModel, Config: map[string]string{"provider": cfg.VoiceWorkerProvider, "model": cfg.VoiceWorkerModel}})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "Completed fixture work."})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	resultID = outcome.Outcome.ResultID
	mu.Unlock()
	if err := agent.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(speech) != 4 || speech[0] != "Ready." || speech[1] != "Typed answer." || speech[2] != "Local fixture acknowledgement." || speech[3] != "Completed fixture work." {
		t.Fatal("selected speech delivery paths", speech)
	}
	var recordings [][]byte
	for _, message := range completion.Messages {
		for _, data := range message.Images {
			recordings = append(recordings, data)
		}
	}
	if len(recordings) != 1 || !bytes.Equal(recordings[0], original) {
		t.Fatal("completion lost actual retained WAV", len(recordings))
	}
	if len(completion.Tools) != 0 || completion.Truncate == nil || *completion.Truncate || completion.Shift == nil || *completion.Shift {
		t.Fatal("unsafe completion constraints")
	}
	if asr.Load() != 0 || maximum.Load() != 1 || sink.frames.Load() != 12 {
		t.Fatal("ASR/overlap/PCM delivery", asr.Load(), maximum.Load(), sink.frames.Load())
	}
	snap := c.Snapshot(scope)
	if len(snap.Pending) != 1 || snap.Pending[0].State != work.Held || len(snap.Communicated) != 0 {
		t.Fatal("software sink falsely claimed audible completion", snap.Pending)
	}
	if len(snap.Jobs) != 1 || snap.Jobs[0].Model != "independent-worker" || !reflect.DeepEqual(cfg, before) {
		t.Fatal("worker/global configuration drift")
	}
	t.Logf("selected speech=quiet+typed+local+completion; ASR=%d; max HTTP bodies=%d; software samples=%d; completion remains held without audible receipt", asr.Load(), maximum.Load(), sink.frames.Load())
}
