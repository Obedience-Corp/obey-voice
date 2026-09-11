package audio

import (
	"context"
	"errors"
	"testing"
)

func TestPCMStreamPublishesFirstWriteBeforeConsumerCanObserveFrame(t *testing.T) {
	const rounds = 256
	for i := 0; i < rounds; i++ {
		stream := NewPCMStream(context.Background())
		if err := stream.SetSampleRate(24000); err != nil {
			t.Fatal(err)
		}
		observed := make(chan bool, 1)
		go func() {
			frame := <-stream.Frames()
			if len(frame) != 1 || frame[0] != 0.25 {
				observed <- false
				return
			}
			// Capture immediately after receiving PCM, before synchronizing
			// with the producer or the test goroutine.
			observed <- !stream.FirstWrittenAt().IsZero()
		}()
		if err := stream.Write([]float32{0.25}); err != nil {
			t.Fatal(err)
		}
		if !<-observed {
			t.Fatalf("round %d: consumer observed PCM before first-write timestamp publication", i)
		}
		stream.Close()
	}
}

func TestPCMStreamDoesNotPublishUnsuccessfulOrEmptyWrites(t *testing.T) {
	stream := NewPCMStream(context.Background())
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	if err := stream.Write(nil); err != nil {
		t.Fatalf("empty Write() error = %v", err)
	}
	if !stream.FirstWrittenAt().IsZero() {
		t.Fatal("empty write published first-write timestamp")
	}
	invalid := NewPCMStream(context.Background())
	if err := invalid.Write([]float32{1}); err == nil {
		t.Fatal("write without sample rate unexpectedly succeeded")
	}
	if !invalid.FirstWrittenAt().IsZero() {
		t.Fatal("invalid write published first-write timestamp")
	}
	closed := NewPCMStream(context.Background())
	if err := closed.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := closed.Write([]float32{1}); err == nil {
		t.Fatal("write after close unexpectedly succeeded")
	}
	if !closed.FirstWrittenAt().IsZero() {
		t.Fatal("closed write published first-write timestamp")
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := NewPCMStream(ctx)
	if err := canceled.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	// Fill the real constructor's buffered channel with successful writes, then
	// cancel a later blocked write. Its first-write evidence must stay stable.
	for i := 0; i < cap(canceled.frames); i++ {
		if err := canceled.Write([]float32{0}); err != nil {
			t.Fatal(err)
		}
	}
	first := canceled.FirstWrittenAt()
	written := canceled.Written()
	cancel()
	if err := canceled.Write([]float32{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Write() error = %v, want context.Canceled", err)
	}
	if got := canceled.FirstWrittenAt(); got.IsZero() || !got.Equal(first) || canceled.Written() != written {
		t.Fatalf("canceled write changed telemetry: first=%v written=%d, want first=%v written=%d", got, canceled.Written(), first, written)
	}
}
