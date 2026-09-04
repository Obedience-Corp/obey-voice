package pipeline

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"testing"
)

type backgroundBrain struct {
	fakeBrain
	opts  brain.StreamOptions
	calls int
}

func (b *backgroundBrain) BackgroundReady() <-chan struct{} { return nil }
func (b *backgroundBrain) BackgroundPending() bool          { return true }
func (b *backgroundBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.opts = opts
	b.calls++
	return scriptedStream(ctx, []string{"The background task is complete."}, nil), nil
}
func TestBackgroundTurnUsesNormalSpeechWithoutUserInputOrTools(t *testing.T) {
	b := &backgroundBrain{}
	bus := events.NewBus()
	users := 0
	responses := 0
	events.Subscribe(bus, func(events.UserInput) { users++ })
	events.Subscribe(bus, func(events.ResponseReady) { responses++ })
	p := &Pipeline{Brain: b, Events: bus, VoiceToolsEnabled: true}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 || !b.opts.Background || b.opts.ToolsEnabled || b.opts.Speaker != "" {
		t.Fatalf("options=%+v calls=%d", b.opts, b.calls)
	}
	if users != 0 || responses != 1 {
		t.Fatalf("users=%d responses=%d", users, responses)
	}
}
