package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func scriptedStreamWithThinking(ctx context.Context, chunks, thinking []string, doneErr error) *brain.Stream {
	out := make(chan string)
	th := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(th)
		defer close(done)
		for _, t := range thinking {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case th <- t:
			}
		}
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case out <- c:
			}
		}
		done <- brain.StreamResult{Err: doneErr}
	}()
	return &brain.Stream{Chunks: out, Thinking: th, Done: done}
}

func ollamaMessageServer(t *testing.T, messages []map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_, _ = io.WriteString(w, `{"models":[{"name":"m"}]}`)
			return
		}
		for i, msg := range messages {
			line, err := json.Marshal(map[string]any{
				"model":   "m",
				"message": msg,
				"done":    i == len(messages)-1,
			})
			if err != nil {
				t.Errorf("marshal message: %v", err)
				return
			}
			_, _ = w.Write(append(line, '\n'))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSilentReasoningTaggedThinkIsNotSpoken(t *testing.T) {
	b, err := brain.NewOllama(&config.Config{
		OllamaHost: ollamaDeltaServer(t, []string{
			"<think>The user said hi. Plan a warm greeting.</think>",
			"Hello there!",
		}),
		OllamaModel: "m",
		AgentName:   "Obey",
		MaxHistory:  10,
	})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var deltas, thinks strings.Builder
	events.Subscribe(p.Events, func(d events.ResponseDelta) { deltas.WriteString(d.Text) })
	events.Subscribe(p.Events, func(d events.ThinkingDelta) { thinks.WriteString(d.Text) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := b.ThinkStream(ctx, "hi", brain.StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream: %v", err)
	}
	metrics := newTurnMetrics()
	transcript, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	if strings.Contains(deltas.String(), "<think>") {
		t.Errorf("ResponseDelta carries <think>: %q", deltas.String())
	}
	if !strings.Contains(thinks.String(), "The user said hi") {
		t.Errorf("ThinkingDelta missing inner text: %q", thinks.String())
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, "The user said hi") || textclean.ContainsThinkTag(spoken) {
		t.Errorf("spoken carries reasoning: %q", spoken)
	}
	if !strings.Contains(transcript, "Hello there") {
		t.Errorf("transcript lost the reply: %q", transcript)
	}
}

func TestSilentReasoningThinkingFieldNotSynthesized(t *testing.T) {
	const thinkingBody = "Okay, the user just asked \"Hey, how's it going?\" and then said \"Okay.\""
	b, err := brain.NewOllama(&config.Config{
		OllamaHost: ollamaMessageServer(t, []map[string]any{
			{"role": "assistant", "thinking": thinkingBody, "content": "Hello there!"},
		}),
		OllamaModel: "m",
		MaxHistory:  10,
	})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var thinks strings.Builder
	events.Subscribe(p.Events, func(d events.ThinkingDelta) { thinks.WriteString(d.Text) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := b.ThinkStream(ctx, "hi", brain.StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream: %v", err)
	}
	metrics := newTurnMetrics()
	transcript, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	if thinks.String() != thinkingBody {
		t.Errorf("ThinkingDelta = %q, want the field", thinks.String())
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, thinkingBody) || strings.Contains(spoken, "just asked") {
		t.Errorf("TTS spoke thinking body: %q", spoken)
	}
	if !strings.Contains(transcript, "Hello there") {
		t.Errorf("transcript = %q, want the greeting", transcript)
	}
}

func TestSilentReasoningSynthQueueNeverCarriesThinkTags(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var segments, voices []string
	events.Subscribe(p.Events, func(e events.SpeechSegmentReady) { segments = append(segments, e.Text) })
	events.Subscribe(p.Events, func(e events.GeneratingVoice) { voices = append(voices, e.Sentence) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{
		"<think>secret plan. More reasoning.</think>Hello there!",
	}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	for dest, items := range map[string][]string{"synth": provider.Calls(), "segment": segments, "voice": voices} {
		for _, s := range items {
			if textclean.ContainsThinkTag(s) {
				t.Errorf("%s item carries think tag: %q", dest, s)
			}
			if strings.Contains(s, "secret plan") {
				t.Errorf("%s item carries reasoning: %q", dest, s)
			}
		}
	}
}

func TestSilentReasoningRecoverStallWithOnlyThinking(t *testing.T) {
	bus := events.NewBus()
	var got []string
	events.Subscribe(bus, func(e events.ResponseReady) { got = append(got, e.Response) })

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	p.recoverTurn(context.Background(), turn, metrics, "", errPlaybackStalled)
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("stall with only thinking: ResponseReady = %q, want empty", got)
	}
}

func TestSilentReasoningThinkingChannelNotSynthesizedWhenNotInThink(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const thinkingBody = "The user just asked how it's going. I should greet them warmly."
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStreamWithThinking(ctx, []string{"Hello there!"}, []string{thinkingBody}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, thinkingBody) || strings.Contains(spoken, "just asked") {
		t.Errorf("synthQueue saw thinking body while InThink is false: %q", spoken)
	}
	if !strings.Contains(spoken, "Hello there") {
		t.Errorf("lost the greeting: %q", spoken)
	}
}

func TestSilentReasoningLeakFixtureNotSynthesized(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var thinkErrs int
	events.Subscribe(p.Events, func(e events.Error) {
		if e.Stage == "think" {
			thinkErrs++
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{"<think>escaped tag still here</think>Hi there."}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if thinkErrs == 0 {
		t.Error("want think-leak Error for escaped tags on Chunks")
	}
	if metrics.thinkLeak == 0 {
		t.Error("want thinkLeak > 0 for escaped tags")
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, "escaped tag") || textclean.ContainsThinkTag(spoken) {
		t.Errorf("leaked tag was synthesized: %q", spoken)
	}
}

type hungReadyTTS struct{}

func (h *hungReadyTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	return audio.NewPCMStream(ctx), nil
}
func (h *hungReadyTTS) Available() bool                       { return true }
func (h *hungReadyTTS) ListVoices(string, string) []tts.Voice { return nil }

// sinkEngine is Engine + SinkReporter for watchdog / TTFA tests.
type sinkEngine struct {
	kind     string
	accepted int
	inner    *fakePlayer
}

func (s *sinkEngine) SinkKind() string   { return s.kind }
func (s *sinkEngine) AcceptedCount() int { return s.accepted }

func (s *sinkEngine) IsPlaying() bool {
	return s.inner != nil && s.inner.IsPlaying()
}

func (s *sinkEngine) Close() error {
	if s.inner != nil {
		return s.inner.Close()
	}
	return nil
}

func (s *sinkEngine) Stop() {
	if s.inner != nil {
		s.inner.Stop()
	}
}

func (s *sinkEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if s.inner != nil {
		return s.inner.PlayStream(ctx, stream)
	}
	_, err := stream.WaitReady(ctx)
	if err != nil {
		return nil, err
	}
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	close(started)
	done <- audio.PlaybackResult{}
	close(done)
	return audio.NewPlayback(started, done), nil
}

var (
	_ audio.Engine       = (*sinkEngine)(nil)
	_ audio.SinkReporter = (*sinkEngine)(nil)
)

func TestSilentReasoningHungWaitReadyStreamClientStalls(t *testing.T) {
	bus := events.NewBus()
	var metricsEvt events.TurnMetrics
	events.Subscribe(bus, func(e events.TurnMetrics) { metricsEvt = e })
	var stallErrs int
	events.Subscribe(bus, func(e events.Error) {
		if e.Stage == "playback" {
			stallErrs++
		}
	})

	p := &Pipeline{
		STT:                  &fakeSTT{text: "hi"},
		Brain:                &stallBrain{},
		TTS:                  &hungReadyTTS{},
		Player:               &sinkEngine{kind: audio.SinkStreamClient, accepted: 1},
		Events:               bus,
		PlaybackStallTimeout: 150 * time.Millisecond,
	}

	start := time.Now()
	_, err := p.RunTurn(context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stall took %v, want within ~8s budget", elapsed)
	}
	if err != nil {
		t.Fatalf("RunTurn err = %v, want nil after stall recover", err)
	}
	if stallErrs == 0 {
		t.Fatal("want playback stall Error (errPlaybackStalled)")
	}
	if !metricsEvt.StallWatchdogFired {
		t.Fatal("StallWatchdogFired = false, want true for stream_client hung WaitReady")
	}
}

func TestSilentReasoningNoSinkHungWaitReadyDoesNotStall(t *testing.T) {
	bus := events.NewBus()
	var ready events.ResponseReady
	events.Subscribe(bus, func(e events.ResponseReady) { ready = e })
	var metricsEvt events.TurnMetrics
	events.Subscribe(bus, func(e events.TurnMetrics) { metricsEvt = e })
	var stallErrs int
	events.Subscribe(bus, func(e events.Error) {
		if e.Stage == "playback" {
			stallErrs++
		}
	})

	p := &Pipeline{
		STT:                  &fakeSTT{text: "hi"},
		Brain:                &stallBrain{},
		TTS:                  &hungReadyTTS{},
		Player:               &sinkEngine{kind: audio.SinkNone},
		Events:               bus,
		PlaybackStallTimeout: 150 * time.Millisecond,
	}

	start := time.Now()
	_, err := p.RunTurn(context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("no-sink hung WaitReady took %v, want within 2s", elapsed)
	}
	if err != nil {
		t.Fatalf("RunTurn err = %v, want nil (no errPlaybackStalled)", err)
	}
	if errors.Is(err, errPlaybackStalled) {
		t.Fatal("no-sink timeout returned errPlaybackStalled")
	}
	if stallErrs != 0 {
		t.Fatalf("playback Error events = %d, want 0", stallErrs)
	}
	if !strings.Contains(ready.Response, "Hello there") {
		t.Fatalf("ResponseReady = %q, want speakable greeting", ready.Response)
	}
	if metricsEvt.StallWatchdogFired {
		t.Fatal("StallWatchdogFired on no-sink timeout")
	}
}

func TestSilentReasoningNoSinkPCMArrivesNoStall(t *testing.T) {
	bus := events.NewBus()
	var metricsEvt events.TurnMetrics
	events.Subscribe(bus, func(e events.TurnMetrics) { metricsEvt = e })
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })

	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:                  &fakeSTT{text: "hi"},
		Brain:                &fakeBrain{chunks: []string{"Hello there."}},
		TTS:                  &fakeTTS{},
		Player:               &sinkEngine{kind: audio.SinkNone, inner: player},
		Events:               bus,
		PlaybackStallTimeout: time.Second,
	}

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn err = %v", err)
	}
	if metricsEvt.StallWatchdogFired {
		t.Fatal("stall with no sink and PCM")
	}
	if metricsEvt.TTFAElapsed != 0 {
		t.Fatalf("TTFAElapsed = %v, want 0", metricsEvt.TTFAElapsed)
	}
	if metricsEvt.AudibleSink != audio.SinkNone {
		t.Fatalf("AudibleSink = %q, want none", metricsEvt.AudibleSink)
	}
	if metricsEvt.PlaybackCompleteElapsed != 0 {
		t.Fatalf("PlaybackCompleteElapsed = %v, want 0 for no sink", metricsEvt.PlaybackCompleteElapsed)
	}
	found := false
	for _, msg := range infos {
		if strings.Contains(msg, "synth drain (no sink)") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("infos = %v, want synth drain (no sink)", infos)
	}
}

func TestSilentReasoningStreamClientAcceptedSetsTTFA(t *testing.T) {
	bus := events.NewBus()
	var metricsEvt events.TurnMetrics
	events.Subscribe(bus, func(e events.TurnMetrics) { metricsEvt = e })
	var started int
	events.Subscribe(bus, func(events.SpeakingStarted) { started++ })

	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hi"},
		Brain:  &fakeBrain{chunks: []string{"Hello there."}},
		TTS:    &fakeTTS{},
		Player: &sinkEngine{kind: audio.SinkStreamClient, accepted: 2, inner: player},
		Events: bus,
	}

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn err = %v", err)
	}
	if started == 0 {
		t.Fatal("Started() / SpeakingStarted not fired at pcmReady")
	}
	if metricsEvt.FirstAudioReadyElapsed <= 0 {
		t.Fatal("FirstAudioReadyElapsed unset — Started() should fire at pcmReady, not at accept")
	}
	if metricsEvt.TTFAElapsed <= 0 {
		t.Fatalf("TTFAElapsed = %v, want set when accepted-count > 0", metricsEvt.TTFAElapsed)
	}
	if metricsEvt.AudibleSink != audio.SinkStreamClient {
		t.Fatalf("AudibleSink = %q, want stream_client", metricsEvt.AudibleSink)
	}
	if metricsEvt.PlaybackCompleteElapsed != 0 {
		t.Fatalf("PlaybackCompleteElapsed = %v, want 0 for stream_client", metricsEvt.PlaybackCompleteElapsed)
	}
}

func TestSilentReasoningStreamClientZeroAcceptLeavesTTFAUnset(t *testing.T) {
	bus := events.NewBus()
	var metricsEvt events.TurnMetrics
	events.Subscribe(bus, func(e events.TurnMetrics) { metricsEvt = e })

	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hi"},
		Brain:  &fakeBrain{chunks: []string{"Hello there."}},
		TTS:    &fakeTTS{},
		Player: &sinkEngine{kind: audio.SinkStreamClient, accepted: 0, inner: player},
		Events: bus,
	}

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn err = %v", err)
	}
	if metricsEvt.FirstAudioReadyElapsed <= 0 {
		t.Fatal("Started() should still fire at pcmReady when nothing accepts")
	}
	if metricsEvt.TTFAElapsed != 0 {
		t.Fatalf("TTFAElapsed = %v, want 0 when accepted-count is 0", metricsEvt.TTFAElapsed)
	}
}

func TestStampTTFAOnFinishedIfLaterChunkAccepted(t *testing.T) {
	player := &sinkEngine{kind: audio.SinkStreamClient, accepted: 0}
	p := &Pipeline{Player: player, Events: events.NewBus()}
	m := newTurnMetrics()
	m.audibleSink = audio.SinkStreamClient
	m.sinkAtArm = audio.SinkStreamClient

	p.applyPlaybackEvent(playbackEvent{kind: playbackStarted, sentence: "hi"}, m, new(atomic.Int64))
	if !m.ttfa.IsZero() {
		t.Fatal("TTFA stamped at pcmReady with accepted-count 0")
	}
	pcmReady := m.firstAudioReady
	if pcmReady.IsZero() {
		t.Fatal("pcmReady / firstAudioReady unset")
	}

	player.accepted = 1
	p.applyPlaybackEvent(playbackEvent{kind: playbackFinished, sentence: "hi"}, m, new(atomic.Int64))
	if m.ttfa.IsZero() {
		t.Fatal("TTFA unset after a later chunk was accepted")
	}
	if !m.ttfa.Equal(pcmReady) {
		t.Fatalf("TTFA %v != pcmReady %v — stamp the existing pcmReady time, not drain", m.ttfa, pcmReady)
	}
}
