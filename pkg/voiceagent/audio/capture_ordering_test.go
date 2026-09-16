package audio

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func orderedPCM(start, n int) []float32 {
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32(start + i)
	}
	return samples
}

func TestCaptureOrderingPreservesCallbackSpansForDelayedReads(t *testing.T) {
	c := NewCapture()
	id, legacy := c.Subscribe(3)
	defer c.Unsubscribe(id)
	// Deliberately old ingestion times make a read-time stamp observably wrong
	// without sleeping or depending on scheduler timing.
	at := time.Now().Add(-5 * time.Second)
	first, second, third := orderedPCM(0, 960), orderedPCM(960, 960), orderedPCM(1920, 1280)
	c.ingestCapture(first, at, 1)
	c.ingestCapture(second, at.Add(60*time.Millisecond), 1)
	c.ingestCapture(third, at.Add(140*time.Millisecond), 1)
	f, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Samples, orderedPCM(0, ChunkSize)) {
		t.Fatal("callback metadata changed PCM")
	}
	if len(f.CaptureSpans) != 2 || f.CaptureSpans[0].SampleCount != 960 || f.CaptureSpans[1].SampleCount != 640 {
		t.Fatalf("read must preserve its two contributing callbacks: %+v", f.CaptureSpans)
	}
	if f.StartedAt != at.Add(-60*time.Millisecond) || f.CaptureSpans[1].CallbackAt != at.Add(60*time.Millisecond) {
		t.Fatalf("delayed read replaced callback timing: %+v", f)
	}
	if f.CaptureSpans[1].SampleOffset != 960 || f.CaptureSpans[0].DeviceGeneration != 1 {
		t.Fatalf("incorrect capture ordering: %+v", f.CaptureSpans)
	}
	next, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(next.Samples, orderedPCM(1600, ChunkSize)) || len(next.CaptureSpans) != 2 {
		t.Fatalf("second read lost PCM or callback boundary: %+v", next.CaptureSpans)
	}
	span := next.CaptureSpans[0]
	if span.SampleOffset != 1600 || span.SampleCount != 320 || span.EstimatedStart != at.Add(40*time.Millisecond) || span.CallbackAt != at.Add(60*time.Millisecond) {
		t.Fatalf("split callback has incorrect retained metadata: %+v", span)
	}
	if f.CaptureSpans[1].SampleCount != 640 { // read metadata must not alias the ring
		t.Fatal("later reads mutated an already returned span")
	}
	for _, want := range [][]float32{first, second, third} {
		if got := <-legacy; !slices.Equal(got, want) {
			t.Fatal("legacy subscription changed callback PCM")
		}
	}
}

type orderingBlockedFrontend struct {
	entered chan struct{}
	release chan struct{}
}

func (f *orderingBlockedFrontend) ProcessCapture(samples []float32) []float32 {
	close(f.entered)
	<-f.release
	return samples
}
func (*orderingBlockedFrontend) PushPlaybackReference([]float32) {}
func (*orderingBlockedFrontend) Close() error                    { return nil }

func TestCaptureOrderingRetainsArrivalBeforeFrontendWork(t *testing.T) {
	c := NewCapture()
	frontend := &orderingBlockedFrontend{entered: make(chan struct{}), release: make(chan struct{})}
	c.SetFrontend(frontend)
	at := time.Now().Add(-time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.ingestCapture(orderedPCM(0, ChunkSize), at, 1)
	}()
	<-frontend.entered
	if c.buf.Available() != 0 {
		t.Error("frontend output was published before processing completed")
	}
	close(frontend.release)
	<-done
	f, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.CaptureSpans[0].CallbackAt != at || f.StartedAt != at.Add(-100*time.Millisecond) {
		t.Fatalf("frontend completion/read time replaced callback entry: %+v", f.CaptureSpans)
	}
}

func TestCaptureOrderingDeviceGenerationPrecedesOpenAndSurvivesRestart(t *testing.T) {
	c := NewCapture()
	at := time.Now().Add(-time.Second)
	opens := 0
	c.open = func() error {
		opens++
		// Model a callback invoked synchronously by device.Start, before open
		// returns. It must already have the new device generation.
		c.ingestCapture(orderedPCM((opens-1)*800, 800), at.Add(time.Duration(opens)*time.Second), c.deviceGeneration.Load())
		return nil
	}
	c.closeDevice = func() {}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	f, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Samples, orderedPCM(0, ChunkSize)) || len(f.CaptureSpans) != 2 {
		t.Fatalf("restart changed buffered PCM or hid its boundary: %+v", f.CaptureSpans)
	}
	old, next := f.CaptureSpans[0], f.CaptureSpans[1]
	if old.DeviceGeneration != 1 || old.DeviceDiscontinuity || next.DeviceGeneration != 2 || !next.DeviceDiscontinuity || next.SampleOffset != 800 {
		t.Fatalf("device generation boundary missing: %+v", f.CaptureSpans)
	}
	if f.CaptureGap != (CaptureGap{}) {
		t.Fatalf("unknown device gap invented a count of missing buffered samples: %+v", f.CaptureGap)
	}
}

func TestCaptureOrderingResetReportsDiscardWithoutRewinding(t *testing.T) {
	c := NewCapture()
	at := time.Now()
	c.ingestCapture(orderedPCM(0, 800), at, 1)
	c.Reset()
	if _, err := c.ReadFrame(t.Context()); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("empty reset read: %v", err)
	}
	c.ingestCapture(orderedPCM(800, ChunkSize), at.Add(time.Second), 1)
	f, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Samples, orderedPCM(800, ChunkSize)) || f.CaptureSpans[0].SampleOffset != 800 {
		t.Fatal("reset changed new PCM or rewound the timeline")
	}
	if f.CaptureGap != (CaptureGap{DiscardedSamples: 800, Reset: true}) {
		t.Fatalf("reset loss was hidden or called overflow: %+v", f.CaptureGap)
	}
	c.ingestCapture(orderedPCM(2400, ChunkSize), at.Add(2*time.Second), 1)
	next, err := c.ReadFrame(t.Context())
	if err != nil || next.CaptureGap != (CaptureGap{}) {
		t.Fatalf("reset loss repeated on later read: %+v, %v", next.CaptureGap, err)
	}
}

func TestCaptureOrderingLegacyWritesDoNotInventCallbackTimes(t *testing.T) {
	c := NewCapture()
	c.buf.Write(orderedPCM(0, ChunkSize))
	if got := c.Read(); !slices.Equal(got, orderedPCM(0, ChunkSize)) {
		t.Fatal("legacy Read changed")
	}
	c.buf.Write(orderedPCM(1600, ChunkSize))
	f, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	span := f.CaptureSpans[0]
	if !f.StartedAt.IsZero() || !span.CallbackAt.IsZero() || !span.EstimatedStart.IsZero() || span.DeviceGeneration != 0 || span.SampleOffset != 1600 {
		t.Fatalf("legacy write fabricated timing/device identity or lost order: %+v", f)
	}
}

func TestCaptureOrderingConcurrentOverflowConservesSampleAccounting(t *testing.T) {
	c := NewCapture()
	c.buf = NewRingBuffer(ChunkSize * 4)
	const writes = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range writes {
			c.ingestCapture(orderedPCM(i*ChunkSize, ChunkSize), time.Now(), 1)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var delivered, overflow uint64
	var previousEnd uint64
	for {
		f, err := c.ReadFrame(ctx)
		if errors.Is(err, ErrNoFrameReady) {
			select {
			case <-done:
				if c.buf.Available() == 0 {
					if delivered+overflow != writes*ChunkSize {
						t.Fatalf("sample accounting delivered=%d overflow=%d, want %d", delivered, overflow, writes*ChunkSize)
					}
					return
				}
			default:
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		start := f.CaptureSpans[0].SampleOffset
		if start != previousEnd+f.CaptureGap.OverflowSamples || !slices.Equal(f.Samples, orderedPCM(int(start), ChunkSize)) {
			t.Fatalf("lost ordering/PCM at %d after %d, gap=%+v", start, previousEnd, f.CaptureGap)
		}
		previousEnd = start + uint64(len(f.Samples))
		delivered += uint64(len(f.Samples))
		overflow += f.CaptureGap.OverflowSamples
	}
}
