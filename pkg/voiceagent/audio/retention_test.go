package audio

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func parkedCapture(t *testing.T, ringSize, unread int) (*Capture, Frame) {
	t.Helper()
	c := NewCapture()
	c.buf = NewRingBuffer(ringSize)
	at := time.Now().Add(-time.Second)
	c.ingestCapture(orderedPCM(0, 960), at, 1)
	c.ingestCapture(orderedPCM(960, 640), at.Add(40*time.Millisecond), 1)
	parked, err := c.ReadFrame(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if unread > 0 {
		c.ingestCapture(orderedPCM(1600, unread), at.Add(100*time.Millisecond), 1)
	}
	return c, parked
}

func TestCaptureRetainForNextListenAcrossParkedAndRing(t *testing.T) {
	for _, tc := range []struct {
		name                                                                string
		unread, keep, parkedStart, ringStart, wantParked, wantRing, discard int
	}{
		{"parked only", 0, 400, 1200, 1600, 400, 0, 1200},
		{"straddles callback", 0, 1000, 600, 1600, 1000, 0, 600},
		{"parked and unread", 320, 800, 1120, 1600, 480, 320, 1120},
		{"ring only", 960, 320, 1600, 2240, 0, 320, 2240},
		{"all available", 320, 3200, 0, 1600, 1600, 320, 0},
		{"none", 320, 0, 1600, 1920, 0, 0, 1920},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, parked := parkedCapture(t, 6400, tc.unread)
			original := append([]CaptureSpan(nil), parked.CaptureSpans...)
			keep := time.Duration(tc.keep) * time.Second / SampleRate
			result, err := c.RetainForNextListen(parked, keep)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(result.Samples, orderedPCM(tc.parkedStart, tc.wantParked)) {
				t.Fatalf("parked=%v want %d samples from%d", result.Samples, tc.wantParked, tc.parkedStart)
			}
			if result.Duration != SamplesDuration(tc.wantParked) || result.Sequence != parked.Sequence || result.CaptureGap.DiscardedSamples != uint64(tc.discard) || result.CaptureGap.Reset != (tc.discard > 0 || tc.keep == 0) || result.CaptureGap.OverflowSamples != 0 {
				t.Fatalf("retained metadata=%+v", result)
			}
			if !slices.Equal(parked.CaptureSpans, original) || !slices.Equal(parked.Samples, orderedPCM(0, 1600)) {
				t.Fatal("caller parked frame mutated")
			}
			if tc.wantParked > 0 {
				if &result.Samples[0] != &parked.Samples[tc.parkedStart] {
					t.Fatal("parked PCM copied")
				}
				span := result.CaptureSpans[0]
				originalSpan := original[0]
				if tc.parkedStart >= 960 {
					originalSpan = original[1]
				}
				if span.SampleOffset != uint64(tc.parkedStart) || span.CallbackAt != originalSpan.CallbackAt || span.EstimatedStart != originalSpan.EstimatedStart.Add(SamplesDuration(tc.parkedStart-int(originalSpan.SampleOffset))) || result.StartedAt != span.EstimatedStart {
					t.Fatalf("sliced timing=%+v", result)
				}
				result.CaptureSpans[0].SampleCount = -1
				if !slices.Equal(parked.CaptureSpans, original) {
					t.Fatal("returned spans alias caller metadata")
				}
			} else if len(result.CaptureSpans) != 0 || !result.StartedAt.IsZero() {
				t.Fatal("empty retained PCM claims callback timing")
			}
			if c.buf.Available() != tc.wantRing {
				t.Fatalf("unread=%d want%d", c.buf.Available(), tc.wantRing)
			}
			if tc.wantRing > 0 {
				pcm, spans, gap := c.buf.readCapture(tc.wantRing)
				if !slices.Equal(pcm, orderedPCM(tc.ringStart, tc.wantRing)) || spans[0].SampleOffset != uint64(tc.ringStart) || gap != (CaptureGap{}) {
					t.Fatalf("ring retention or duplicate gap: pcm=%v spans=%+v gap=%+v", pcm, spans, gap)
				}
			}
		})
	}
}

func TestCaptureRetentionPreservesOverflowAndRemovedGenerationBoundary(t *testing.T) {
	c, parked := parkedCapture(t, 1600, 0)
	c.ingestCapture(orderedPCM(1600, 1600), time.Now(), 2)
	c.ingestCapture(orderedPCM(3200, 1600), time.Now(), 2) // transition span overwritten
	result, err := c.RetainForNextListen(parked, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Empty() || result.CaptureGap != (CaptureGap{OverflowSamples: 1600, DiscardedSamples: 2400, Reset: true, DeviceDiscontinuity: true}) {
		t.Fatalf("actual loss masked as retention: %+v", result)
	}
	pcm, _, gap := c.buf.readCapture(800)
	if !slices.Equal(pcm, orderedPCM(4000, 800)) || gap != (CaptureGap{}) {
		t.Fatalf("wrong tail or loss duplicated: %v %+v", pcm, gap)
	}
}

func TestCaptureRetentionInvalidParkedFrameLeavesRingIntact(t *testing.T) {
	for name, mut := range map[string]func(*Capture, *Frame){
		"no metadata":    func(_ *Capture, f *Frame) { f.CaptureSpans = nil },
		"wrong count":    func(_ *Capture, f *Frame) { f.CaptureSpans[0].SampleCount-- },
		"wrong order":    func(_ *Capture, f *Frame) { f.CaptureSpans[1].SampleOffset++ },
		"final":          func(_ *Capture, f *Frame) { f.Final = true },
		"wrong rate":     func(_ *Capture, f *Frame) { f.SampleRate = 24000 },
		"another reader": func(c *Capture, _ *Frame) { c.buf.Read(160) },
	} {
		t.Run(name, func(t *testing.T) {
			c, parked := parkedCapture(t, 6400, 320)
			mut(c, &parked)
			before := c.buf.Available()
			if _, err := c.RetainForNextListen(parked, 0); err == nil {
				t.Fatal("invalid source ordering accepted")
			}
			if c.buf.Available() != before {
				t.Fatal("failed retention mutated unread PCM")
			}
		})
	}
}

func TestCaptureRetentionReturnsHistoricalGapsOnceAndClampsWindow(t *testing.T) {
	c, parked := parkedCapture(t, 64000, 0)
	parked.CaptureGap = CaptureGap{OverflowSamples: 12, SubscriptionDroppedSamples: 7, SubscriptionDroppedCallbacks: 1, SubscriptionDeviceDiscontinuity: true}
	c.ingestCapture(orderedPCM(1600, 40000), time.Now(), 1)
	result, err := c.RetainForNextListen(parked, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if c.buf.Available() != 32000 || result.CaptureGap.OverflowSamples != 12 || result.CaptureGap.SubscriptionDroppedSamples != 7 || !result.CaptureGap.SubscriptionDeviceDiscontinuity || result.CaptureGap.DiscardedSamples != 9600 {
		t.Fatalf("bounded retention lost earlier evidence: %+v", result)
	}
	_, _, gap := c.buf.readCapture(1)
	if gap != (CaptureGap{}) {
		t.Fatal("transferred gap repeated")
	}
}

func TestCaptureRetentionConcurrentCallbacksConserveTailOrder(t *testing.T) {
	c, parked := parkedCapture(t, 64000, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			c.ingestCapture(orderedPCM(1600+i*160, 160), time.Now(), 1)
		}
	}()
	result, err := c.RetainForNextListen(parked, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	remaining := c.buf.Available()
	pcm, spans, gap := c.buf.readCapture(remaining)
	all := append(append([]float32(nil), result.Samples...), pcm...)
	start := int(result.CaptureGap.DiscardedSamples)
	if !slices.Equal(all, orderedPCM(start, 4800-start)) || gap != (CaptureGap{}) {
		t.Fatalf("retention raced callback source ordering: start=%d count=%d gap=%+v", start, len(all), gap)
	}
	if len(result.Samples) > 0 && len(spans) > 0 && result.CaptureSpans[len(result.CaptureSpans)-1].SampleOffset+uint64(result.CaptureSpans[len(result.CaptureSpans)-1].SampleCount) != spans[0].SampleOffset {
		t.Fatal("parked and unread are not contiguous")
	}
}

func TestFinalizableForwardsRetentionWithoutClearingFinalize(t *testing.T) {
	c, parked := parkedCapture(t, 6400, 320)
	wrapped := NewFinalizable(c)
	wrapped.Finalize()
	if !wrapped.CanRetainForNextListen() {
		t.Fatal("capture capability lost")
	}
	result, err := wrapped.RetainForNextListen(parked, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !wrapped.Pending() || len(result.Samples) != 480 || c.buf.Available() != 320 {
		t.Fatal("forwarding changed finalization or retention")
	}
	source := &scriptedSource{frames: []Frame{liveFrame(10)}}
	unsupported := NewFinalizable(source)
	if unsupported.CanRetainForNextListen() {
		t.Fatal("unsupported source advertised retention")
	}
	if _, err := unsupported.RetainForNextListen(Frame{}, time.Second); !errors.Is(err, ErrRetentionUnsupported) {
		t.Fatalf("unsupported retention error=%v", err)
	}
	if source.reads != 0 || NewFinalizable(unsupported).CanRetainForNextListen() {
		t.Fatal("capability probe consumed audio or hid unsupported wrapped source")
	}
}

func TestCaptureRetentionPreservesGenerationAfterRingReset(t *testing.T) {
	c, parked := parkedCapture(t, 6400, 0)
	c.ingestCapture(orderedPCM(1600, 160), time.Now(), 2)
	c.Reset() // transition callback no longer has an unread span
	result, err := c.RetainForNextListen(parked, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Empty() || result.CaptureGap != (CaptureGap{DiscardedSamples: 1760, Reset: true, DeviceDiscontinuity: true}) {
		t.Fatalf("reset masked physical discontinuity: %+v", result)
	}
	c.ingestCapture(orderedPCM(1760, 160), time.Now(), 2)
	_, _, gap := c.buf.readCapture(160)
	if gap != (CaptureGap{}) {
		t.Fatalf("transferred reset counted twice: %+v", gap)
	}
}
