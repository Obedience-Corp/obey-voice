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
	// resetStarted/resetRelease hold LiveAdapter.Reset inside the analyzer call
	// it makes while holding the adapter mutex. That is the only way a test can
	// put a known, controlled duration in front of a Submit.
	resetStarted chan struct{}
	resetRelease chan struct{}
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

func (f *liveFake) Reset() error {
	if f.resetStarted != nil {
		select {
		case f.resetStarted <- struct{}{}:
		default:
		}
	}
	if f.resetRelease != nil {
		<-f.resetRelease
	}
	return nil
}

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
}

// The response-path total is a sum, and one Submit cannot show that: with a
// single sample an accumulator and a plain store are indistinguishable, which
// is what the earlier `total >= last` check amounted to. Two samples with
// independent floors separate them — under a store the total would equal the
// second sample alone, which is strictly less than the two together.
func TestLiveAdapterAccumulatesResponsePathTime(t *testing.T) {
	big := make([]float32, 1<<20) // 4 MiB: cannot be copied inside a clock tick

	// The analyzer never returns, so neither frame is consumed and nothing but
	// Submit touches the counters. Close cancels the context the fake selects on.
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 4)
	defer a.Close()

	if err := a.Submit(context.Background(), Segment{ID: "first", Samples: big}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	first := a.Stats().LastResponsePathNanos
	if first < uint64(time.Microsecond) {
		t.Fatalf("first submit recorded %dns for a 4 MiB frame, want at least 1µs", first)
	}

	if err := a.Submit(context.Background(), Segment{ID: "second", Samples: big}); err != nil {
		t.Fatal(err)
	}
	stats := a.Stats()
	if stats.LastResponsePathNanos < uint64(time.Microsecond) {
		t.Fatalf("second submit recorded %dns for a 4 MiB frame, want at least 1µs", stats.LastResponsePathNanos)
	}
	if want := first + stats.LastResponsePathNanos; stats.ResponsePathNanos < want {
		t.Fatalf("response-path total %s is below the two samples it took in (%s + %s): it is storing, not accumulating",
			time.Duration(stats.ResponsePathNanos), time.Duration(first), time.Duration(stats.LastResponsePathNanos))
	}
}

// A dropped submit is the one call that never reaches the queue, and it was
// covered only by accident: the old `!= 0` block ran with the dropped Submit as
// the last one, so removing that block left the drop path's accounting asserted
// nowhere. It cannot be floored by work — the drop returns before it copies
// anything — but the response-path clock starts before the mutex is taken, so a
// lock held for a known time is a floor the test can create. That is also the
// honest claim: this counter measures what the caller waited for.
func TestLiveAdapterTimesADroppedSubmit(t *testing.T) {
	const lockHeld = 20 * time.Millisecond

	fake := &liveFake{
		started:      make(chan struct{}, 1),
		release:      make(chan struct{}),
		resetStarted: make(chan struct{}, 1),
		resetRelease: make(chan struct{}),
	}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()

	// Saturate: the worker takes the first frame and blocks in the analyzer, the
	// second fills the one-deep queue, so the third has nowhere to go.
	if err := a.Submit(context.Background(), Segment{ID: "held", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if err := a.Submit(context.Background(), Segment{ID: "queued", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}

	// Read the baseline first: Stats takes the same mutex, so asking for it
	// after Reset has the lock would spend the whole hold here instead.
	before := a.Stats()

	// Reset calls the analyzer while holding the adapter mutex, so the dropped
	// Submit below waits on the lock for as long as the fake holds it.
	resetDone := make(chan error, 1)
	go func() { resetDone <- a.Reset() }()
	<-fake.resetStarted
	go func() {
		time.Sleep(lockHeld)
		close(fake.resetRelease)
	}()

	if err := a.Submit(context.Background(), Segment{ID: "dropped", Samples: []float32{1}}); !errors.Is(err, ErrLiveDropped) {
		t.Fatalf("saturated submit error = %v, want ErrLiveDropped", err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("Reset: %v", err)
	}

	stats := a.Stats()
	if stats.Dropped != before.Dropped+1 {
		t.Errorf("dropped = %d, want %d", stats.Dropped, before.Dropped+1)
	}
	if stats.LastResponsePathNanos < uint64(lockHeld) {
		t.Errorf("the dropped submit recorded %s while it waited at least %s on the lock: the drop path returns without being timed",
			time.Duration(stats.LastResponsePathNanos), lockHeld)
	}
	if stats.ResponsePathNanos < before.ResponsePathNanos+stats.LastResponsePathNanos {
		t.Errorf("response-path total %s did not take in the dropped submit's %s",
			time.Duration(stats.ResponsePathNanos), time.Duration(stats.LastResponsePathNanos))
	}
	// It never reached the analyzer, so nothing about it may land there.
	if stats.Processed != before.Processed || stats.AnalyzerNanos != before.AnalyzerNanos {
		t.Errorf("a dropped frame moved the analyzer counters: processed %d->%d, nanos %d->%d",
			before.Processed, stats.Processed, before.AnalyzerNanos, stats.AnalyzerNanos)
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
