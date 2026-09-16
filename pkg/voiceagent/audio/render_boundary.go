package audio

import (
	"sync/atomic"
	"time"
)

// FirstRenderBoundary records the first device-buffer write for one playback
// attempt, shared by all its segments. It is a callback observation, not a DAC
// timestamp or proof that the listener heard the output. The zero value is ready
// for use. A boundary must never be reset or copied after use.
type FirstRenderBoundary struct {
	state atomic.Uint32 // 0: unpublished, 1: writer owns at, 2: immutable
	at    time.Time
}

// Observe publishes once, without allocating or waiting. Call only inside a
// valid playback fence after copying a nonempty segment into the device buffer.
// Queuing, synthesis, idle silence and rejected writes must not publish.
func (b *FirstRenderBoundary) Observe(at time.Time) {
	if b == nil || at.IsZero() || !b.state.CompareAndSwap(0, 1) {
		return
	}
	b.at = at
	b.state.Store(2)
}

// FirstRender returns a stable timestamp with its monotonic component intact.
// false means no published observation, including a writer currently publishing;
// callers must not interpret that as proof capture preceded playback.
func (b *FirstRenderBoundary) FirstRender() (time.Time, bool) {
	if b == nil || b.state.Load() != 2 {
		return time.Time{}, false
	}
	return b.at, true
}
