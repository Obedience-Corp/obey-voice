package voiceagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type closingNativeAgentBrain struct {
	stubBrain
	closed atomic.Int32
}

func (b *closingNativeAgentBrain) Close() error { b.closed.Add(1); return nil }

func nativeAgentFixture(t *testing.T) (*NativeCandidate, *closingNativeAgentBrain, *atomic.Int32) {
	t.Helper()
	media := chatmedia.NewAudioSession()
	t.Cleanup(media.Close)
	responder := &closingNativeAgentBrain{}
	speechClosed := &atomic.Int32{}
	candidate := &NativeCandidate{Brain: responder, Voice: &TTSSet{Primary: &stubTTS{}}, Policy: &pipeline.NativePolicy{Lifetime: t.Context(), Controls: "conversation", Transcript: "off", Media: media}, Setup: nativeaudio.SetupResult{ProtocolReady: true}, cfg: &config.Config{STTProvider: "must-not-load-standard-recognizer", ModelsDir: t.TempDir(), AgentName: "Native"}, closeSpeech: func() { speechClosed.Add(1) }}
	t.Cleanup(func() { _ = candidate.Close() })
	return candidate, responder, speechClosed
}
func TestNativeAgentUsesPreparedResourcesAndClosesOwnedProvidersOnce(t *testing.T) {
	candidate, responder, speechClosed := nativeAgentFixture(t)
	stages := []string{}
	opts := Options{Events: events.NewBus(), Capture: &stubCapture{}, Input: &fixtureInput{}, Player: &stubPlayer{}, Worker: &stubBrain{}, Stage: func(stage string) { stages = append(stages, stage) }}
	agent, cleanup, err := candidate.NewAgent(t.Context(), opts, func(context.Context, *NativeCandidate) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if agent.Brain != responder || agent.Native != candidate.Policy || agent.Input != opts.Input || agent.STT != nil {
		t.Fatal("native resources were substituted")
	}
	if len(stages) != 0 {
		t.Fatal("constructor loaded a Standard provider or device", stages)
	}
	cleanup()
	cleanup()
	if responder.closed.Load() != 1 || speechClosed.Load() != 1 {
		t.Fatal("native resources did not close exactly once", responder.closed.Load(), speechClosed.Load())
	}
}
func TestNativeAgentRejectsUnreadyAndConflictingOptionsBeforeAcquisition(t *testing.T) {
	candidate, _, _ := nativeAgentFixture(t)
	opts := Options{Events: events.NewBus(), Capture: &stubCapture{}, Input: &fixtureInput{}, Silent: true, Worker: &stubBrain{}}
	if _, _, err := candidate.NewAgent(t.Context(), opts, nil); err == nil {
		t.Fatal("protocol readiness authorized activation")
	}
	cause := errors.New("fixture integration not ready")
	if _, _, err := candidate.NewAgent(t.Context(), opts, func(context.Context, *NativeCandidate) error { return cause }); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	opts.Config = &config.Config{}
	if _, _, err := candidate.NewAgent(t.Context(), opts, func(context.Context, *NativeCandidate) error {
		t.Fatal("invalid options reached authorization")
		return nil
	}); err == nil {
		t.Fatal("prepared config overridden")
	}
	opts.Config = nil
	opts.Capture = nil
	if _, _, err := candidate.NewAgent(t.Context(), opts, func(context.Context, *NativeCandidate) error { return nil }); err == nil {
		t.Fatal("injected input opened implicit microphone")
	}
}
