package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCaptureReadFrameNoFrameReadyThenLive(t *testing.T) {
	c := NewCapture() // ring buffer only; no device started
	ctx := context.Background()

	// Empty buffer reports no-frame-ready, NOT end-of-input.
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame() on empty capture = %v, want ErrNoFrameReady", err)
	}

	// Feed a chunk as the device callback would.
	c.buf.Write(make([]float32, ChunkSize))
	f, err := c.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() with buffered audio error = %v", err)
	}
	if f.Final {
		t.Error("live frame Final = true, want false (live never finalizes on silence)")
	}
	if f.SourceKind != SourceLive {
		t.Errorf("live frame SourceKind = %q, want %q", f.SourceKind, SourceLive)
	}
	if len(f.Samples) != ChunkSize {
		t.Errorf("live frame samples = %d, want %d", len(f.Samples), ChunkSize)
	}
	if f.Sequence != 1 {
		t.Errorf("live frame Sequence = %d, want 1", f.Sequence)
	}

	// Drained again: back to no-frame-ready, still never Final.
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Errorf("drained capture = %v, want ErrNoFrameReady", err)
	}
}

func TestCaptureReadFrameCancellation(t *testing.T) {
	c := NewCapture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFrame() on canceled ctx = %v, want context.Canceled", err)
	}
}

func TestCaptureResetClearsPartialFrame(t *testing.T) {
	c := NewCapture()
	c.buf.Write(make([]float32, ChunkSize/2))

	c.Reset()

	if got := c.buf.Available(); got != 0 {
		t.Fatalf("buffered samples after Reset = %d, want 0", got)
	}
	if _, err := c.ReadFrame(context.Background()); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame after partial reset = %v, want ErrNoFrameReady", err)
	}
}

func TestCaptureCloseIsSafeWhenNotRunning(t *testing.T) {
	c := NewCapture()
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

func TestCaptureStalledAfterSilence(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	if c.stalled() {
		t.Fatal("fresh capture must not look stalled")
	}
	c.lastData.Store(time.Now().Add(-3 * time.Second).UnixNano())
	if !c.stalled() {
		t.Fatal("want stalled after 3s without callbacks")
	}
	c.lastData.Store(time.Now().UnixNano())
	if c.stalled() {
		t.Fatal("fresh callback must clear stalled")
	}
}

func TestCaptureRecoverIfStalledNoopsWhenStopped(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	c.lastData.Store(time.Now().Add(-time.Hour).UnixNano())
	if err := c.recoverIfStalled(context.Background()); err != nil {
		t.Fatalf("recoverIfStalled on a stopped capture: %v", err)
	}
	if c.IsRunning() {
		t.Fatal("must not open a device from a stopped capture")
	}
}

// TestCapturePublishUnsubscribeNoPanic is the send-on-closed-channel regression
// guard: the malgo callback publishes while subscribers churn. Pre-fix,
// Unsubscribe closed the channel outside the lock while publish sent outside
// the lock, so a send could land on a just-closed channel and panic the audio
// callback thread.
func TestCapturePublishUnsubscribeNoPanic(t *testing.T) {
	c := NewCapture()
	samples := []float32{0.1, 0.2, 0.3}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 2000 {
			c.publish(samples)
		}
	}()

	for range 500 {
		id, ch := c.Subscribe(1)
		// Drain a little so the buffered channel stays send-ready.
		select {
		case <-ch:
		default:
		}
		c.Unsubscribe(id)
	}
	<-done
}
