package audio

import (
	"sync"
	"testing"
	"time"
)

func TestFirstRenderBoundaryConcurrentPublication(t *testing.T) {
	var b FirstRenderBoundary
	at := time.Now()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				b.Observe(at)
				if got, ok := b.FirstRender(); ok && got != at {
					t.Errorf("published partial or changed timestamp: %v", got)
				}
			}
		})
	}
	wg.Wait()
	b.Observe(at.Add(time.Second))
	if got, ok := b.FirstRender(); !ok || got != at {
		t.Fatalf("first observation/monotonic component changed: %v, %t", got, ok)
	}
}

func TestFirstRenderBoundaryDoesNotAllocateOnCallback(t *testing.T) {
	var b FirstRenderBoundary
	if got := testing.AllocsPerRun(100, func() {
		// No concurrent readers: reset only this test fixture between trials.
		b.state.Store(0)
		b.Observe(time.Now())
	}); got != 0 {
		t.Fatalf("callback observation allocated: %v", got)
	}
}

func TestFirstRenderBoundaryUnknown(t *testing.T) {
	var nilBoundary *FirstRenderBoundary
	nilBoundary.Observe(time.Now())
	if _, ok := nilBoundary.FirstRender(); ok {
		t.Fatal("nil boundary reported a write")
	}
	var b FirstRenderBoundary
	b.Observe(time.Time{})
	if _, ok := b.FirstRender(); ok {
		t.Fatal("zero timestamp reported a write")
	}
	b.state.Store(1)
	if _, ok := b.FirstRender(); ok {
		t.Fatal("unfinished publication exposed its timestamp")
	}
}
