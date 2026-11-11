package playback

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Stop fences every attempt bound before it, including a re-bind of the same
// context. Only an explicit rebind admits the caller's next utterance.
func TestRebindAdmitsNewSpeechAfterStop(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	p.SetVolume(1)
	p.testOpen = func(rate int) (nativeOutput, int, int, error) {
		return &fakeOutput{}, rate, 2, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ctx = p.BindPlaybackContext(ctx)
	p.Stop()

	stale := p.BindPlaybackContext(ctx)
	if _, err := p.PlayStream(stale, bufferedAttempt(t, stale, []float32{.25, .25})); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped binding was re-admitted: %v", err)
	}
	fresh := p.RebindPlaybackContext(ctx)
	if _, err := p.PlayStream(fresh, bufferedAttempt(t, fresh, []float32{.25, .25})); err != nil {
		t.Fatalf("rebound speech refused: %v", err)
	}
	p.Stop()
	if _, err := p.PlayStream(fresh, bufferedAttempt(t, fresh, []float32{.25, .25})); !errors.Is(err, context.Canceled) {
		t.Fatalf("rebound binding survived a later stop: %v", err)
	}
}
