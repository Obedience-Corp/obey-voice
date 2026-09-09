//go:build !integration

package meeting

import (
	"context"
	"fmt"
	"sync"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

// DeferredAnalyzer builds the real analyzer on the first Finalize call.
// Meeting record uses this so pyannote + TitaNet stay off the process during
// capture: live labels (when on) already own one embedding session, and
// review only needs the offline models after stop. Close before Finalize
// never constructs them.
type DeferredAnalyzer struct {
	mu     sync.Mutex
	build  func(context.Context) (SpeakerAnalyzer, error)
	inner  SpeakerAnalyzer
	err    error
	closed bool
	built  bool
}

// NewDeferredAnalyzer returns a SpeakerAnalyzer that runs build at most once.
// build must be non-nil.
func NewDeferredAnalyzer(build func(context.Context) (SpeakerAnalyzer, error)) *DeferredAnalyzer {
	if build == nil {
		panic("meeting: NewDeferredAnalyzer requires a builder")
	}
	return &DeferredAnalyzer{build: build}
}

// Finalize constructs the inner analyzer if needed, then forwards.
func (d *DeferredAnalyzer) Finalize(ctx context.Context, samples []float32) (speaker.Timeline, error) {
	inner, err := d.ensure(ctx)
	if err != nil {
		return speaker.Timeline{}, err
	}
	return inner.Finalize(ctx, samples)
}

// Close releases a constructed inner analyzer. If Finalize never ran, this is
// a no-op: native models were never loaded.
func (d *DeferredAnalyzer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if !d.built || d.inner == nil {
		return nil
	}
	if closer, ok := d.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (d *DeferredAnalyzer) ensure(ctx context.Context) (SpeakerAnalyzer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("meeting: speaker analyzer closed")
	}
	if d.built {
		return d.inner, d.err
	}
	d.built = true
	inner, err := d.build(ctx)
	if err != nil {
		d.err = err
		return nil, err
	}
	if inner == nil {
		d.err = fmt.Errorf("meeting: deferred speaker analyzer returned nil")
		return nil, d.err
	}
	d.inner = inner
	return d.inner, nil
}
