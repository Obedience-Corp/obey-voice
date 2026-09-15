package audio

import (
	"slices"
	"testing"
	"time"
)

func TestRingCaptureOverflowRetainsExactPCMAndSpans(t *testing.T) {
	rb := NewRingBuffer(10)
	at := time.Now()
	rb.writeCapture(orderedPCM(0, 8), at, 1)
	rb.writeCapture(orderedPCM(8, 6), at.Add(time.Millisecond), 1)
	pcm, spans, gap := rb.readCapture(8)
	if !slices.Equal(pcm, orderedPCM(4, 8)) || gap != (CaptureGap{OverflowSamples: 4}) {
		t.Fatalf("overflow PCM=%v gap=%+v", pcm, gap)
	}
	if len(spans) != 2 || spans[0].SampleOffset != 4 || spans[0].SampleCount != 4 || spans[1].SampleOffset != 8 || spans[1].SampleCount != 4 {
		t.Fatalf("overflow lost callback boundaries: %+v", spans)
	}
	if spans[0].EstimatedStart != at.Add(-SamplesDuration(4)) {
		t.Fatalf("partial eviction lost estimated sample timing: %+v", spans[0])
	}
	pcm, spans, gap = rb.readCapture(2)
	if !slices.Equal(pcm, orderedPCM(12, 2)) || spans[0].SampleOffset != 12 || gap != (CaptureGap{}) {
		t.Fatalf("remaining read PCM=%v spans=%+v gap=%+v", pcm, spans, gap)
	}
}

func TestRingCaptureKeepLastReportsIntentionalDiscard(t *testing.T) {
	rb := NewRingBuffer(8)
	at := time.Now()
	rb.writeCapture(orderedPCM(0, 6), at, 1)
	if got := rb.Read(2); !slices.Equal(got, orderedPCM(0, 2)) {
		t.Fatal("legacy read changed PCM")
	}
	rb.KeepLast(2)
	rb.writeCapture(orderedPCM(6, 4), at.Add(time.Millisecond), 1)
	pcm, spans, gap := rb.readCapture(6)
	if !slices.Equal(pcm, orderedPCM(4, 6)) || spans[0].SampleOffset != 4 || gap != (CaptureGap{DiscardedSamples: 2, Reset: true}) {
		t.Fatalf("retention PCM=%v spans=%+v gap=%+v", pcm, spans, gap)
	}
}

func TestRingCaptureMetadataBoundedByUnreadPCM(t *testing.T) {
	rb := NewRingBuffer(8)
	at := time.Now()
	for i := range 1000 {
		rb.writeCapture([]float32{float32(i)}, at.Add(time.Duration(i)*time.Millisecond), 1)
		if len(rb.spans) > rb.Available() {
			t.Fatalf("metadata grew beyond retained PCM: spans=%d samples=%d", len(rb.spans), rb.Available())
		}
	}
	pcm, spans, gap := rb.readCapture(8)
	if !slices.Equal(pcm, orderedPCM(992, 8)) || spans[0].SampleOffset != 992 || gap.OverflowSamples != 992 {
		t.Fatalf("wrapped ring lost exact sample accounting: pcm=%v spans=%+v gap=%+v", pcm, spans, gap)
	}
	if len(rb.spans) != 0 {
		t.Fatal("drained PCM retained stale metadata")
	}
}
