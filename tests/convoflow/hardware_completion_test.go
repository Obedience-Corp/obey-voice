package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCompletionListenStartsAfterColdWorkerReadiness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ready := make(chan struct{})
		go func() { time.Sleep(23 * time.Second); close(ready) }()
		var listen context.Context
		var cancel context.CancelFunc
		starts := 0
		elapsed, err := beginCompletionListenAfterWorker(t.Context(), func() bool {
			select {
			case <-ready:
				return true
			default:
				return false
			}
		}, func() {
			starts++
			select {
			case <-ready:
			default:
				t.Error("listen started while worker was still running")
			}
			// Model the real recognizer's configured 10-second input deadline. Its
			// lifetime starts at the actual production helper's launch boundary.
			listen, cancel = context.WithTimeout(t.Context(), 10*time.Second)
		})
		if cancel != nil {
			defer cancel()
		}
		if err != nil || starts != 1 || elapsed < 23*time.Second || elapsed > 23*time.Second+10*time.Millisecond {
			t.Fatalf("readiness=%s starts=%d err=%v", elapsed, starts, err)
		}
		if listen.Err() != nil {
			t.Fatalf("cold worker consumed listening deadline: %v", listen.Err())
		}
		deadline, _ := listen.Deadline()
		if deadline.Sub(start) < 33*time.Second {
			t.Fatalf("listening deadline included worker wait: %s", deadline.Sub(start))
		}
		// The already-ready path launches exactly once without another wait.
		elapsed, err = beginCompletionListenAfterWorker(t.Context(), func() bool { return true }, func() { starts++ })
		if err != nil || elapsed != 0 || starts != 2 {
			t.Fatalf("ready worker restart: elapsed=%s starts=%d err=%v", elapsed, starts, err)
		}
	})
}

func TestCompletionWorkerReadinessTimeoutNeverStartsListening(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		starts := 0
		elapsed, err := beginCompletionListenAfterWorker(t.Context(), func() bool { return false }, func() { starts++ })
		if err == nil || !strings.Contains(err.Error(), "background result did not become pending") || elapsed != 30*time.Second || starts != 0 {
			t.Fatalf("deadline=%s starts=%d err=%v", elapsed, starts, err)
		}
	})
}

func TestCompletionWorkerReadinessHonorsOuterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		starts := 0
		elapsed, err := beginCompletionListenAfterWorker(ctx, func() bool { return false }, func() { starts++ })
		if !errors.Is(err, context.DeadlineExceeded) || elapsed != time.Second || starts != 0 {
			t.Fatalf("canceled wait=%s starts=%d err=%v", elapsed, starts, err)
		}
		// A result appearing after cancellation must not start an expired session.
		_, err = beginCompletionListenAfterWorker(ctx, func() bool { return true }, func() { starts++ })
		if !errors.Is(err, context.DeadlineExceeded) || starts != 0 {
			t.Fatalf("canceled ready state started listening: starts=%d err=%v", starts, err)
		}
	})
}
