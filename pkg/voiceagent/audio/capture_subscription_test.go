package audio

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestCaptureSubscriptionFramesMatchCallbackAndRingPositions(t *testing.T) {
	c := NewCapture()
	id, frames := c.SubscribeFrames(3)
	defer c.Unsubscribe(id)
	otherID, other := c.SubscribeFrames(3)
	defer c.Unsubscribe(otherID)
	legacyID, legacy := c.Subscribe(3)
	defer c.Unsubscribe(legacyID)
	if id == otherID || id == legacyID || otherID == legacyID {
		t.Fatal("subscription IDs collide")
	}
	at := time.Now().Add(-time.Hour)
	first, second := orderedPCM(0, 960), orderedPCM(960, 2240)
	c.ingestCapture(first, at, 1)
	c.ingestCapture(second, at.Add(140*time.Millisecond), 1)
	var delivered []Frame
	for i, pcm := range [][]float32{first, second} {
		f := <-frames
		peer := <-other
		old := <-legacy
		if !slices.Equal(f.Samples, pcm) || !slices.Equal(old, pcm) || &f.Samples[0] != &old[0] || &f.Samples[0] != &peer.Samples[0] {
			t.Fatal("subscription changed callback PCM or copying contract")
		}
		if len(f.CaptureSpans) != 1 || &f.CaptureSpans[0] != &peer.CaptureSpans[0] {
			t.Fatal("callback metadata not shared as immutable span")
		}
		want := CaptureSpan{SampleOffset: uint64(i * 960), SampleCount: len(pcm), DeviceGeneration: 1, CallbackAt: at.Add(time.Duration(i) * 140 * time.Millisecond)}
		want.EstimatedStart = want.CallbackAt.Add(-SamplesDuration(len(pcm)))
		if f.CaptureSpans[0] != want || f.StartedAt != want.EstimatedStart || f.Duration != SamplesDuration(len(pcm)) || f.Sequence != int64(i+1) || f.SampleRate != SampleRate || f.Channels != Channels || f.SourceKind != SourceLive || f.Final || f.CaptureGap != (CaptureGap{}) {
			t.Fatalf("frame metadata=%+v", f)
		}
		delivered = append(delivered, f)
	}
	for read := range 2 {
		f, err := c.ReadFrame(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.Samples, orderedPCM(read*ChunkSize, ChunkSize)) || f.Sequence != int64(read+1) {
			t.Fatal("subscription changed ring PCM or read sequence")
		}
		for _, span := range f.CaptureSpans {
			callback := delivered[0].CaptureSpans[0]
			if span.SampleOffset >= 960 {
				callback = delivered[1].CaptureSpans[0]
			}
			if want := sliceCaptureSpan(callback, span.SampleOffset, span.SampleOffset+uint64(span.SampleCount)); span != want {
				t.Fatalf("ring and callback metadata differ: %+v / %+v", span, want)
			}
		}
	}
	if delivered[1].CaptureSpans[0].SampleCount != 2240 {
		t.Fatal("ring pruning mutated published metadata")
	}
}

func TestCaptureSubscriptionLossIndependentOfRingResetAndGeneration(t *testing.T) {
	c := NewCapture()
	c.buf = NewRingBuffer(4)
	slowID, slow := c.SubscribeFrames(1)
	defer c.Unsubscribe(slowID)
	fastID, fast := c.SubscribeFrames(8)
	defer c.Unsubscribe(fastID)
	at := time.Now()
	c.ingestCapture(orderedPCM(0, 3), at, 1)
	c.ingestCapture(orderedPCM(3, 5), at.Add(time.Millisecond), 2) // transition dropped by slow
	c.Reset()
	c.ingestCapture(orderedPCM(8, 2), at.Add(2*time.Millisecond), 2)
	first := <-slow
	if first.CaptureGap != (CaptureGap{}) || first.CaptureSpans[0].SampleOffset != 0 {
		t.Fatal("future loss altered queued frame")
	}
	c.ingestCapture(orderedPCM(10, 6), at.Add(3*time.Millisecond), 2)
	f := <-slow
	wantGap := CaptureGap{SubscriptionDroppedSamples: 7, SubscriptionDroppedCallbacks: 2, SubscriptionDeviceDiscontinuity: true}
	if f.CaptureGap != wantGap || f.CaptureSpans[0].SampleOffset != 10 || f.CaptureSpans[0].SampleCount != 6 || f.CaptureSpans[0].DeviceDiscontinuity || f.Sequence != 4 || !slices.Equal(f.Samples, orderedPCM(10, 6)) {
		t.Fatalf("subscriber loss or span=%+v", f)
	}
	for i := range 4 {
		f := <-fast
		if f.CaptureGap != (CaptureGap{}) {
			t.Fatalf("slow loss contaminated fast subscriber: %+v", f.CaptureGap)
		}
		if f.CaptureSpans[0].DeviceDiscontinuity != (i == 1) {
			t.Fatal("original generation transition changed")
		}
	}
	pcm, spans, gap := c.buf.readCapture(4)
	if !slices.Equal(pcm, orderedPCM(12, 4)) || spans[0].SampleOffset != 12 || gap != (CaptureGap{OverflowSamples: 8, DiscardedSamples: 4, Reset: true}) {
		t.Fatalf("ring loss contaminated by subscriptions: samples=%v spans=%+v gap=%+v", pcm, spans, gap)
	}
	c.ResetKeeping(0)
	c.ingestCapture(orderedPCM(16, 1), at.Add(4*time.Millisecond), 3)
	f = <-slow
	if f.CaptureGap != (CaptureGap{}) || !f.CaptureSpans[0].DeviceDiscontinuity || f.CaptureSpans[0].SampleOffset != 16 {
		t.Fatalf("loss repeated or reset rewound offsets: %+v", f)
	}
}

func TestCaptureSubscriptionConcurrentUnsubscribeAndIngest(t *testing.T) {
	c := NewCapture()
	c.buf = NewRingBuffer(16)
	id, keep := c.SubscribeFrames(2048)
	defer c.Unsubscribe(id)
	var wg sync.WaitGroup
	for writer := range 2 {
		wg.Go(func() {
			for i := range 200 {
				c.ingestCapture([]float32{float32(writer*200 + i)}, time.Now(), 1)
			}
		})
	}
	for range 3 {
		wg.Go(func() {
			for range 100 {
				id, ch := c.SubscribeFrames(0)
				c.Unsubscribe(id)
				c.Unsubscribe(id)
				for range ch {
				} // buffered frames may precede channel closure
				oldID, old := c.Subscribe(1)
				c.Unsubscribe(oldID)
				for range old {
				}
				c.Reset()
				if err := c.Close(); err != nil {
					t.Error(err)
				} // no device was opened
			}
		})
	}
	wg.Wait()
	for i := range 400 {
		f := <-keep
		if f.Sequence != int64(i+1) || f.CaptureSpans[0].SampleOffset != uint64(i) || f.CaptureGap != (CaptureGap{}) {
			t.Fatalf("concurrent callback order/loss=%+v", f)
		}
	}
}

func TestCaptureSubscriptionEmptyCallbackDoesNotInventSpan(t *testing.T) {
	c := NewCapture()
	id, ch := c.SubscribeFrames(-1)
	defer c.Unsubscribe(id)
	c.ingestCapture(nil, time.Now(), 1)
	select {
	case f := <-ch:
		t.Fatalf("empty callback fabricated source metadata: %+v", f)
	default:
	}
	c.ingestCapture([]float32{0.25}, time.Now(), 1)
	f := <-ch
	if f.Sequence != 1 || f.CaptureSpans[0].SampleOffset != 0 || f.Samples[0] != 0.25 {
		t.Fatal("empty callback advanced source timeline")
	}
}
