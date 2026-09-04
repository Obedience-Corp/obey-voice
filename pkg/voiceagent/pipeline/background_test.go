package pipeline

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"strings"
	"testing"
	"time"
)

type backgroundBrain struct {
	fakeBrain
	opts  brain.StreamOptions
	calls int
	acked int
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

func (b *backgroundBrain) AckBackgroundDelivery() { b.acked++ }
func TestBackgroundSpeechBargeInPreservesCaptureAndPendingDelivery(t *testing.T) {
	b := &backgroundBrain{}
	player := newFakePlayer(2 * time.Second)
	defer player.Close()
	capture := newFakeCapture()
	p := &Pipeline{Brain: b, TTS: &fakeTTS{delay: time.Millisecond}, Player: player, Capture: capture, VAD: &fakeVAD{}, BargeInVAD: &fakeVAD{}, Events: events.NewBus()}
	done := make(chan error, 1)
	go func() { done <- p.RunBackgroundTurn(context.Background()) }()
	select {
	case <-player.StartedSignal():
	case <-time.After(time.Second):
		t.Fatal("background did not speak")
	}
	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(60 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background ignored barge-in")
	}
	if player.StopCount() == 0 || !p.keepCapture || capture.ResetCount() != 0 {
		t.Fatalf("stop=%d keepCapture=%v reset=%d", player.StopCount(), p.keepCapture, capture.ResetCount())
	}
	if b.acked != 0 {
		t.Fatal("interrupted brief acknowledged before playback completed")
	}
}

type backgroundHangBrain struct{ hangBrain }

func (b *backgroundHangBrain) BackgroundReady() <-chan struct{} { return nil }
func (b *backgroundHangBrain) BackgroundPending() bool          { return true }
func TestBackgroundBrainTimeoutRecoversInsteadOfSilentCancel(t *testing.T) {
	bus := events.NewBus()
	var report events.Error
	var response events.ResponseReady
	events.Subscribe(bus, func(e events.Error) { report = e })
	events.Subscribe(bus, func(e events.ResponseReady) { response = e })
	p := &Pipeline{Brain: &backgroundHangBrain{}, Events: bus, BrainTurnTimeout: 40 * time.Millisecond}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatalf("expected degraded recovery: %v", err)
	}
	if !strings.Contains(report.Message, "timed out") || !response.Degraded {
		t.Fatalf("error=%+v response=%+v", report, response)
	}
}
