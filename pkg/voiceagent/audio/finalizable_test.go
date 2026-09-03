package audio

import (
	"context"
	"errors"
	"testing"
)

// scriptedSource hands out fixed frames in order, then blocks on ctx.
type scriptedSource struct {
	frames []Frame
	reads  int
	closed bool
}

func (s *scriptedSource) ReadFrame(ctx context.Context) (Frame, error) {
	s.reads++
	if len(s.frames) == 0 {
		<-ctx.Done()
		return Frame{}, ctx.Err()
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

func (s *scriptedSource) Close() error {
	s.closed = true
	return nil
}

func liveFrame(n int) Frame {
	return Frame{Samples: make([]float32, n), SampleRate: SampleRate, Channels: Channels, SourceKind: SourceLive}
}

func TestFinalizablePassesFramesThroughUntilAsked(t *testing.T) {
	src := &scriptedSource{frames: []Frame{liveFrame(10), liveFrame(20)}}
	f := NewFinalizable(src)

	got, err := f.ReadFrame(t.Context())
	if err != nil || len(got.Samples) != 10 || got.Final {
		t.Fatalf("first read = %+v, %v; want the source's frame", got, err)
	}
	f.Finalize()
	if !f.Pending() {
		t.Fatal("Finalize did not leave a pending request")
	}
	got, err = f.ReadFrame(t.Context())
	if err != nil || !got.Final || len(got.Samples) != 0 {
		t.Fatalf("read after Finalize = %+v, %v; want one empty Final frame", got, err)
	}
	if f.Pending() {
		t.Fatal("the Final read did not consume the request")
	}
	// The request was one-shot: the source is read again afterwards.
	got, err = f.ReadFrame(t.Context())
	if err != nil || got.Final || len(got.Samples) != 20 {
		t.Fatalf("read after the Final = %+v, %v; want the source's next frame", got, err)
	}
	if src.reads != 2 {
		t.Errorf("source read %d times, want 2 (the Final never touched it)", src.reads)
	}
}

func TestFinalizableClearDropsThePendingRequest(t *testing.T) {
	src := &scriptedSource{frames: []Frame{liveFrame(5)}}
	f := NewFinalizable(src)
	f.Finalize()
	f.Clear()
	got, err := f.ReadFrame(t.Context())
	if err != nil || got.Final {
		t.Fatalf("read after Clear = %+v, %v; want the source's frame", got, err)
	}
}

func TestFinalizableFinalWinsOverABlockedSource(t *testing.T) {
	// A source with nothing buffered blocks; a pending Finalize must not.
	src := &scriptedSource{}
	f := NewFinalizable(src)
	f.Finalize()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	got, err := f.ReadFrame(ctx)
	if err != nil || !got.Final {
		t.Fatalf("read = %+v, %v; want a Final frame without touching the blocked source", got, err)
	}
}

func TestFinalizableLegacyReadStarvesWhilePending(t *testing.T) {
	src := &scriptedSource{frames: []Frame{liveFrame(5), liveFrame(5)}}
	f := NewFinalizable(src)
	if got := f.Read(); len(got) != 5 {
		t.Fatalf("Read = %d samples, want 5", len(got))
	}
	f.Finalize()
	if got := f.Read(); got != nil {
		t.Fatalf("Read while a Finalize is pending returned %d samples, want none", len(got))
	}
	if src.reads != 1 {
		t.Errorf("the starved Read touched the source (%d reads)", src.reads)
	}
}

func TestFinalizableCloseClosesTheSource(t *testing.T) {
	src := &scriptedSource{}
	f := NewFinalizable(src)
	if err := f.Close(); err != nil || !src.closed {
		t.Fatalf("Close: err=%v closed=%v", err, src.closed)
	}
	// Errors from the source pass through untouched.
	f2 := NewFinalizable(&scriptedSource{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f2.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("read on a canceled ctx = %v, want context.Canceled from the source", err)
	}
}
