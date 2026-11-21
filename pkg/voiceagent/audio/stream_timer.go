package audio

import (
	"fmt"
	"time"
)

// StreamStallTimer bounds a PCM consumer's startup wait and then the gaps
// between batches. The consumer owns the timer and stops it when done.
type StreamStallTimer struct {
	timer       *time.Timer
	budget, gap time.Duration
	started     bool
}

// NewStallTimer starts timing before the sample rate or first PCM is ready.
func (s *PCMStream) NewStallTimer() *StreamStallTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &StreamStallTimer{gap: s.props.stallBudget}
	t.reset(s.props.firstAudioBudget)
	return t
}

// C fires when the current phase times out. A nil channel disables that phase.
func (t *StreamStallTimer) C() <-chan time.Time {
	if t == nil || t.timer == nil {
		return nil
	}
	return t.timer.C
}

// ReceivedFrames begins the next gap after a nonempty batch of PCM arrives.
func (t *StreamStallTimer) ReceivedFrames() {
	t.started = true
	t.reset(t.gap)
}

func (t *StreamStallTimer) reset(budget time.Duration) {
	t.budget = budget
	switch {
	case budget <= 0:
		t.Stop()
		t.timer = nil
	case t.timer == nil:
		t.timer = time.NewTimer(budget)
	default:
		t.timer.Reset(budget)
	}
}

// Err describes the silence that caused the timer to fire.
func (t *StreamStallTimer) Err() error {
	if t.started {
		return fmt.Errorf("%w: no further audio for %s", ErrStreamStalled, t.budget)
	}
	return fmt.Errorf("%w: waited %s for first audio", ErrStreamStalled, t.budget)
}

func (t *StreamStallTimer) Stop() {
	if t.timer != nil {
		t.timer.Stop()
	}
}
