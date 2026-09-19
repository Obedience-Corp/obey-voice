package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// TestShouldFallbackCoversSlowEngines is the regression for turns that produced
// text and no audio at all. A worker that times out is classified
// ProviderErrorWorker but still unwraps to context.DeadlineExceeded, and the
// old cause-first check refused the fallback for exactly that error — so a
// merely slow Qwen never reached the configured Kokoro voice.
func TestShouldFallbackCoversSlowEngines(t *testing.T) {
	workerTimeout := &tts.ProviderError{
		Provider:  "qwen3-tts",
		Operation: "run worker",
		Kind:      tts.ProviderErrorWorker,
		Err:       fmt.Errorf("worker timed out: %w", context.DeadlineExceeded),
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stalled stream", fmt.Errorf("segment: %w", audio.ErrStreamStalled), true},
		{"worker timeout wrapping a deadline", workerTimeout, true},
		{"worker failure", &tts.ProviderError{Kind: tts.ProviderErrorWorker, Err: errors.New("boom")}, true},
		{"unavailable", &tts.ProviderError{Kind: tts.ProviderErrorUnavailable, Err: errors.New("closed")}, true},
		{"malformed", &tts.ProviderError{Kind: tts.ProviderErrorMalformed, Err: errors.New("bad pcm")}, true},
		// Cancellation is the user's decision, not an engine fault: speaking
		// after a barge-in is worse than staying quiet.
		{"provider canceled", &tts.ProviderError{Kind: tts.ProviderErrorCanceled, Err: context.Canceled}, false},
		{"bare cancel", context.Canceled, false},
		{"unclassified", errors.New("something else"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFallback(tc.err); got != tc.want {
				t.Fatalf("shouldFallback(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBoundPrimarySilenceUsesProviderGrace checks the budget only lands on
// engines that publish one. Kokoro declares no grace, so its streams stay
// unbounded exactly as before this change.
func TestBoundPrimarySilenceUsesProviderGrace(t *testing.T) {
	ctx := context.Background()

	p := &Pipeline{TTS: &graceTTS{grace: 12 * time.Second}}
	stream := audio.NewPCMStream(ctx)
	p.boundPrimarySilence(stream, 40)
	if got := stream.StallBudget(); got != 12*time.Second {
		t.Fatalf("StallBudget() = %v, want the provider grace 12s", got)
	}

	p = &Pipeline{TTS: &fakeTTS{}}
	stream = audio.NewPCMStream(ctx)
	p.boundPrimarySilence(stream, 40)
	if got := stream.StallBudget(); got != 0 {
		t.Fatalf("StallBudget() = %v, want 0 for a provider that publishes no grace", got)
	}

	p.boundPrimarySilence(nil, 40) // must not panic
}

// TestStallTimeoutLeavesRoomForTheFallbackVoice pins the ordering between the
// two recovery mechanisms. The stream budget fails a silent primary over to the
// fallback at the provider grace; if the watchdog abandoned the turn at that
// same instant it would race the rescue, and a lost race is a turn with no
// audio — the bug this is all fixing.
func TestStallTimeoutLeavesRoomForTheFallbackVoice(t *testing.T) {
	graceful := &graceTTS{grace: 40 * time.Second}

	withFallback := &Pipeline{TTS: graceful, TTSFallback: &fakeTTS{}}
	got := withFallback.stallTimeout(0)
	if got <= 40*time.Second {
		t.Fatalf("stallTimeout() = %v, want more than the 40s grace so the fallback can speak first", got)
	}
	if want := 40*time.Second + fallbackSpeakAllowance; got != want {
		t.Fatalf("stallTimeout() = %v, want %v", got, want)
	}

	// With no fallback configured there is nothing to wait for, so the budget
	// stays at the provider grace rather than delaying recovery.
	noFallback := &Pipeline{TTS: graceful}
	if got := noFallback.stallTimeout(0); got != 40*time.Second {
		t.Fatalf("stallTimeout() = %v, want the bare 40s grace without a fallback", got)
	}
}

// TestPlayFallbackRescuesAStalledPrimary is the end of the chain this change
// builds: the real player fails a silent segment with ErrStreamStalled (see
// TestPumpSegmentFailsStalledStream), and the pipeline must answer that by
// speaking the same sentence in the fallback voice instead of dropping it.
// Before this change the stall arrived as a cancellation and the sentence was
// simply never heard.
func TestPlayFallbackRescuesAStalledPrimary(t *testing.T) {
	ctx := context.Background()
	fallback := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: &graceTTS{grace: time.Second}, TTSFallback: fallback, Player: player}

	var stick fallbackLatch
	stalled := fmt.Errorf("segment: %w", audio.ErrStreamStalled)
	playback, usedFallback, err := p.playFallback(ctx, "the sentence that stalled", stalled, &stick)
	if err != nil {
		t.Fatalf("playFallback() error = %v, want the fallback voice to take over", err)
	}
	if !usedFallback {
		t.Fatal("playFallback() reported no fallback; the stalled sentence would be silently dropped")
	}
	if playback == nil {
		t.Fatal("playFallback() returned no playback handle")
	}
	if !stick.latched() {
		t.Fatal("stickFallback did not latch; the rest of the reply would flip between voices")
	}
	if got := fallback.Texts(); len(got) != 1 || got[0] != "the sentence that stalled" {
		t.Fatalf("fallback spoke %q, want the stalled sentence", got)
	}
}
