package nativeaudio

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestForegroundCancelsAndJoinsCaptionBeforeDispatch(t *testing.T) {
	scheduler := NewScheduler()
	started, stopped := make(chan struct{}), make(chan struct{})
	if !scheduler.StartCaption(t.Context(), func(ctx context.Context) { close(started); <-ctx.Done(); close(stopped) }) {
		t.Fatal("caption not started")
	}
	<-started
	if err := scheduler.Run(t.Context(), func(context.Context) error {
		select {
		case <-stopped:
		default:
			t.Fatal("foreground overlapped caption")
		}
		if scheduler.StartCaption(t.Context(), func(context.Context) { t.Error("caption ran during foreground") }) {
			t.Fatal("caption admitted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestForegroundRequestsNeverOverlap(t *testing.T) {
	scheduler := NewScheduler()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	var active atomic.Int32
	go func() {
		done <- scheduler.Run(t.Context(), func(context.Context) error { active.Add(1); close(started); <-release; active.Add(-1); return nil })
	}()
	<-started
	go func() {
		done <- scheduler.Run(t.Context(), func(context.Context) error {
			if active.Add(1) != 1 {
				t.Error("overlapping producers")
			}
			active.Add(-1)
			return nil
		})
	}()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
func TestUnjoinedCaptionFencesBinding(t *testing.T) {
	scheduler := NewScheduler()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if !scheduler.StartCaption(t.Context(), func(context.Context) { close(started); <-release; close(finished) }) {
		t.Fatal("caption not started")
	}
	<-started
	began := time.Now()
	err := scheduler.Run(t.Context(), func(context.Context) error { t.Error("dispatched over stuck caption"); return nil })
	close(release)
	<-finished
	if !errors.Is(err, ErrSchedulerUnavailable) || time.Since(began) > CaptionJoinTimeout+time.Second {
		t.Fatal(err, time.Since(began))
	}
	if err := scheduler.Run(t.Context(), func(context.Context) error { t.Error("unavailable binding dispatched"); return nil }); !errors.Is(err, ErrSchedulerUnavailable) {
		t.Fatal(err)
	}
}
