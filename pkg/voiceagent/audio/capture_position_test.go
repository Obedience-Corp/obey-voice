package audio

import (
	"context"
	"slices"
	"sync"
	"testing"
)

func TestCaptureReadFramePreservesRingSamplePosition(t *testing.T) {
	c := NewCapture()
	c.buf.WriteWithPosition([]float32{1, 2, 3})
	c.buf.WriteWithPosition(make([]float32, ChunkSize-3))
	frame, err := c.ReadFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !frame.SamplePositionKnown || frame.SampleOffset != 0 {
		t.Fatalf("frame position known=%t offset=%d", frame.SamplePositionKnown, frame.SampleOffset)
	}
	if len(frame.Samples) != ChunkSize || frame.Samples[0] != 1 || frame.Samples[2] != 3 {
		t.Fatalf("frame samples lost aggregation: first=%v len=%d", frame.Samples[:3], len(frame.Samples))
	}
}

func TestCapturePositionedFanoutAndUnsubscribe(t *testing.T) {
	c := NewCapture()
	id, frames := c.SubscribeFrames(2)
	idLegacy, legacy := c.Subscribe(2)
	samples := []float32{.1, .2}
	c.publishFrame(Frame{Samples: samples, SampleRate: SampleRate, Channels: Channels, SampleOffset: 9, SamplePositionKnown: true, SourceKind: SourceLive})
	got := <-frames
	if !got.SamplePositionKnown || got.SampleOffset != 9 || !slices.Equal(got.Samples, samples) {
		t.Fatalf("positioned fanout=%+v", got)
	}
	if gotLegacy := <-legacy; !slices.Equal(gotLegacy, samples) {
		t.Fatalf("legacy fanout=%v", gotLegacy)
	}
	c.Unsubscribe(id)
	c.Unsubscribe(idLegacy)
	if _, ok := <-frames; ok {
		t.Fatal("positioned channel remained open")
	}
	if _, ok := <-legacy; ok {
		t.Fatal("legacy channel remained open")
	}
	c.publishFrame(Frame{Samples: []float32{1}})
}

func TestCapturePositionedSubscribeUnsubscribeDuringPublish(t *testing.T) {
	c := NewCapture()
	var wg sync.WaitGroup
	started := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		for i := 0; i < 1000; i++ {
			id, ch := c.SubscribeFrames(1)
			c.Unsubscribe(id)
			<-ch
		}
	}()
	<-started
	for i := 0; i < 1000; i++ {
		c.publishFrame(Frame{Samples: []float32{float32(i)}, SampleOffset: uint64(i), SamplePositionKnown: true})
	}
	wg.Wait()
}
