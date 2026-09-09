//go:build !integration

package meeting

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

type countingAnalyzer struct {
	finalizes atomic.Int32
	closes    atomic.Int32
	timeline  speaker.Timeline
}

func (a *countingAnalyzer) Finalize(context.Context, []float32) (speaker.Timeline, error) {
	a.finalizes.Add(1)
	return a.timeline, nil
}

func (a *countingAnalyzer) Close() error {
	a.closes.Add(1)
	return nil
}

func TestDeferredAnalyzerDoesNotBuildUntilFinalize(t *testing.T) {
	var builds atomic.Int32
	inner := &countingAnalyzer{timeline: speaker.Timeline{Observations: []speaker.Observation{{
		Label: "speaker-1", State: speaker.StateStable,
	}}}}
	deferred := NewDeferredAnalyzer(func(context.Context) (SpeakerAnalyzer, error) {
		builds.Add(1)
		return inner, nil
	})

	if builds.Load() != 0 {
		t.Fatalf("builder ran at construction: %d", builds.Load())
	}
	if err := deferred.Close(); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 0 {
		t.Fatalf("Close before Finalize loaded native models: %d builds", builds.Load())
	}
	if inner.closes.Load() != 0 {
		t.Fatalf("Close before Finalize closed an inner analyzer that was never built")
	}
}

func TestDeferredAnalyzerBuildsOnceOnFinalize(t *testing.T) {
	var builds atomic.Int32
	inner := &countingAnalyzer{timeline: speaker.Timeline{Observations: []speaker.Observation{{
		Label: "speaker-1", State: speaker.StateStable,
	}}}}
	deferred := NewDeferredAnalyzer(func(context.Context) (SpeakerAnalyzer, error) {
		builds.Add(1)
		return inner, nil
	})

	got, err := deferred.Finalize(context.Background(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observations) != 1 || got.Observations[0].Label != "speaker-1" {
		t.Fatalf("timeline = %+v", got)
	}
	if _, err := deferred.Finalize(context.Background(), []float32{0.2}); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 {
		t.Fatalf("builder ran %d times, want 1", builds.Load())
	}
	if inner.finalizes.Load() != 2 {
		t.Fatalf("inner Finalize count = %d, want 2", inner.finalizes.Load())
	}
	if err := deferred.Close(); err != nil {
		t.Fatal(err)
	}
	if inner.closes.Load() != 1 {
		t.Fatalf("inner Close count = %d, want 1", inner.closes.Load())
	}
}

// SpeakerSession.Finalize closes the analyzer and runMeetingRecord's deferred
// SpeakerSession.Close closes it again, so the double Close is the normal path.
func TestDeferredAnalyzerCloseIsIdempotent(t *testing.T) {
	inner := &countingAnalyzer{}
	deferred := NewDeferredAnalyzer(func(context.Context) (SpeakerAnalyzer, error) {
		return inner, nil
	})
	if _, err := deferred.Finalize(context.Background(), []float32{0.1}); err != nil {
		t.Fatal(err)
	}
	if err := deferred.Close(); err != nil {
		t.Fatal(err)
	}
	if err := deferred.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if inner.closes.Load() != 1 {
		t.Fatalf("inner Close count = %d, want 1", inner.closes.Load())
	}
}

func TestDeferredAnalyzerSurfacesBuilderError(t *testing.T) {
	want := errors.New("load pyannote")
	deferred := NewDeferredAnalyzer(func(context.Context) (SpeakerAnalyzer, error) {
		return nil, want
	})
	_, err := deferred.Finalize(context.Background(), []float32{0.1})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	// A later Finalize must not retry a failed build (would reload native models
	// after a permanent init failure).
	_, err = deferred.Finalize(context.Background(), []float32{0.1})
	if !errors.Is(err, want) {
		t.Fatalf("second error = %v, want the same builder failure", err)
	}
}

func TestDeferredAnalyzerRejectsFinalizeAfterClose(t *testing.T) {
	deferred := NewDeferredAnalyzer(func(context.Context) (SpeakerAnalyzer, error) {
		t.Fatal("builder must not run after Close")
		return &countingAnalyzer{}, nil
	})
	if err := deferred.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deferred.Finalize(context.Background(), []float32{0.1}); err == nil {
		t.Fatal("Finalize after Close succeeded")
	}
}
