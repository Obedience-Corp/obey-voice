package speaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type liveFake struct {
	started chan struct{}
	release chan struct{}
	label   string
	err     error
}

func (f *liveFake) IdentifySegment(ctx context.Context, seg Segment) (Observation, error) {
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		case <-f.release:
		}
	}
	if f.err != nil {
		return Observation{}, f.err
	}
	label := f.label
	if label == "" && len(seg.Samples) > 0 && seg.Samples[0] > 1 {
		label = "bob"
	}
	if label == "" {
		label = "alice"
	}
	return Observation{SegmentID: seg.ID, StartMS: MS(seg.Start), EndMS: MS(seg.End), Label: label, Confidence: .9, State: StateStable, Source: seg.Source}, nil
}

func (f *liveFake) Reset() error { return nil }

func TestLiveAdapterDropsWithoutBlockingAndCopiesFrames(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	samples := []float32{1}
	if err := a.Submit(context.Background(), Segment{ID: "one", Samples: samples}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	// The caller reuses its capture buffer the moment Submit returns. The
	// analyzer is holding this frame right now, so an uncopied slice hands it
	// 99 - which liveFake labels "bob" rather than "alice".
	samples[0] = 99
	if err := a.Submit(context.Background(), Segment{ID: "two", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{ID: "three", Samples: []float32{1}}); !errors.Is(err, ErrLiveDropped) {
		t.Fatalf("saturated submit error = %v, want ErrLiveDropped", err)
	}
	if stats := a.Stats(); stats.Dropped != 1 || stats.QueueDepth != 1 {
		t.Fatalf("stats = %+v, want one dropped and one queued", stats)
	}
	close(fake.release)
	awaitProcessed(t, a, 2)

	first := awaitLiveEvent(t, a.Events())
	if first.Observation.SegmentID != "one" {
		t.Fatalf("first event is for %q, want the first submitted frame", first.Observation.SegmentID)
	}
	if first.Observation.Label != "alice" {
		t.Fatalf("frame one was labelled %q, want alice: Submit handed the analyzer the caller's slice instead of a copy",
			first.Observation.Label)
	}
}

// Both paths must be measured. This used to ride on the end of the drop test
// as `!= 0` on four counters, which races the wall clock in the one direction
// a test cannot control: a submit that returns inside a single clock tick
// measures 0 ns honestly, and LastResponsePathNanos did (~1 run in 12 under
// full-suite load). The floors here are ones the test creates itself, so a
// slower machine only makes them safer.
func TestLiveAdapterRecordsAnalyzerAndResponsePathTimings(t *testing.T) {
	const analyzerHeld = 20 * time.Millisecond
	// Submit copies the PCM before queuing it, so a window this size cannot
	// complete inside a clock tick however fast the machine is.
	big := make([]float32, 1<<20) // 4 MiB

	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 2)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "held", Samples: big}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if got := a.Stats().LastResponsePathNanos; got < uint64(time.Microsecond) {
		t.Fatalf("response path recorded %dns for a 4 MiB frame, want at least 1µs: Submit is not being timed", got)
	}

	// The analyzer is inside IdentifySegment for at least this long, so its
	// own counters cannot be below it either.
	time.Sleep(analyzerHeld)
	close(fake.release)
	awaitProcessed(t, a, 1)

	stats := a.Stats()
	if stats.AnalyzerNanos < uint64(analyzerHeld) || stats.LastAnalyzerNanos < uint64(analyzerHeld) {
		t.Fatalf("analyzer timings = %s total / %s last, want at least %s: the held call was not measured",
			time.Duration(stats.AnalyzerNanos), time.Duration(stats.LastAnalyzerNanos), analyzerHeld)
	}
	// The totals are sums, so neither can sit below the sample it just took in.
	if stats.ResponsePathNanos < stats.LastResponsePathNanos {
		t.Fatalf("response-path total %s is below its last sample %s: submits are not accumulated",
			time.Duration(stats.ResponsePathNanos), time.Duration(stats.LastResponsePathNanos))
	}
}

// awaitProcessed waits for the analysis worker to reach want frames. Stats is
// the only observable the worker publishes, and Processed is written after
// IdentifySegment returns, so polling it is what "the analyzer is done" means.
func awaitProcessed(t *testing.T, a *LiveAdapter, want uint64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := a.Stats().Processed; got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("processed = %d, want %d", a.Stats().Processed, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestLiveAdapterResponsePathDoesNotWaitForAnalyzer(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "slow", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	<-fake.started

	started := time.Now()
	if err := a.Submit(context.Background(), Segment{ID: "queued", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("response-path submit took %s while analyzer was blocked", elapsed)
	}
	if got := a.Stats().Processed; got != 0 {
		t.Fatalf("processed = %d before analyzer release, want 0", got)
	}
	close(fake.release)
}

func TestLiveAdapterDisabledBaselineIsIndependent(t *testing.T) {
	a := NewLiveAdapter(context.Background(), nil, 1)
	defer a.Close()
	started := time.Now()
	err := a.Submit(context.Background(), Segment{ID: "baseline", Samples: []float32{1}})
	if !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("disabled submit = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("disabled baseline submit took %s", elapsed)
	}
	if stats := a.Stats(); stats.Processed != 0 || stats.AnalyzerNanos != 0 {
		t.Fatalf("disabled baseline stats = %+v", stats)
	}
}

func TestLiveAdapterEventsAreAsyncAndSessionOrdered(t *testing.T) {
	a := NewLiveAdapter(context.Background(), &liveFake{}, 2)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "one", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	first := awaitLiveEvent(t, a.Events())
	if first.Kind != EventSpeakerUpdated || first.SessionID != "session-1" || first.Sequence != 1 {
		t.Fatalf("first = %+v", first)
	}
	if err := a.Submit(context.Background(), Segment{ID: "two", Samples: []float32{2}}); err != nil {
		t.Fatal(err)
	}
	second := awaitLiveEvent(t, a.Events())
	if second.Kind != EventSpeakerChanged || second.Sequence != 2 {
		t.Fatalf("second = %+v", second)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{ID: "three", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	third := awaitLiveEvent(t, a.Events())
	if third.SessionID != "session-2" || third.Sequence != 1 {
		t.Fatalf("reset event = %+v", third)
	}
	a.SetEnabled(false)
	if err := a.Submit(context.Background(), Segment{ID: "four"}); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("disabled submit = %v", err)
	}
}

func TestLiveAdapterDropsInFlightResultsAcrossReset(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "old", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	close(fake.release)
	select {
	case ev := <-a.Events():
		t.Fatalf("stale event after reset = %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if err := a.Submit(context.Background(), Segment{ID: "new", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	ev := awaitLiveEvent(t, a.Events())
	if ev.Observation.SegmentID != "new" || ev.SessionID != "session-2" || ev.Sequence != 1 {
		t.Fatalf("new session event = %+v", ev)
	}
}

func TestLiveAdapterPreservesErrorContextAndUnavailableState(t *testing.T) {
	fake := &liveFake{err: errors.New("engine unavailable")}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "failed", Start: time.Second, End: 2 * time.Second, Source: SourceLocalMic}); err != nil {
		t.Fatal(err)
	}
	ev := awaitLiveEvent(t, a.Events())
	if ev.Kind != EventSpeakerUpdated || ev.Observation.SegmentID != "failed" || ev.Observation.StartMS != 1000 || ev.Observation.EndMS != 2000 || ev.Observation.Source != SourceLocalMic || ev.Observation.State != StateRejected {
		t.Fatalf("error event = %+v", ev)
	}

	unavailable := NewLiveAdapter(context.Background(), nil, 1)
	defer unavailable.Close()
	unavailable.SetEnabled(true)
	if got := unavailable.Stats().Status; got != LiveUnavailable {
		t.Fatalf("unavailable status after enable = %q", got)
	}
}

func TestLiveAdapterUnavailableAndClose(t *testing.T) {
	a := NewLiveAdapter(context.Background(), nil, 1)
	if got := a.Stats().Status; got != LiveUnavailable {
		t.Fatalf("status = %q", got)
	}
	if err := a.Submit(context.Background(), Segment{}); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("unavailable submit = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{}); !errors.Is(err, ErrLiveClosed) {
		t.Fatalf("closed submit = %v", err)
	}
	select {
	case _, ok := <-a.Events():
		if ok {
			t.Fatal("events should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("events did not close")
	}
}

func BenchmarkLiveAdapterSubmit(b *testing.B) {
	b.Run("disabled_baseline", func(b *testing.B) {
		a := NewLiveAdapter(context.Background(), nil, 1)
		defer a.Close()
		segment := Segment{ID: "baseline", Samples: []float32{1}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := a.Submit(context.Background(), segment); !errors.Is(err, ErrLiveDisabled) {
				b.Fatalf("submit = %v", err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(a.Stats().LastResponsePathNanos), "last-submit-ns")
	})

	b.Run("enabled_overloaded", func(b *testing.B) {
		fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
		a := NewLiveAdapter(context.Background(), fake, 1)
		defer func() {
			close(fake.release)
			a.Close()
		}()
		if err := a.Submit(context.Background(), Segment{ID: "blocking", Samples: []float32{1}}); err != nil {
			b.Fatal(err)
		}
		<-fake.started
		segment := Segment{ID: "overloaded", Samples: []float32{1}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = a.Submit(context.Background(), segment)
		}
		b.StopTimer()
		b.ReportMetric(float64(a.Stats().LastResponsePathNanos), "last-submit-ns")
	})
}

func awaitLiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
		return Event{}
	}
}
