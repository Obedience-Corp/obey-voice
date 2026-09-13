package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPinnedRuntimeBudgetStopsLocallyEvenIfRunIgnoresCancel(t *testing.T) {
	table := NewTable(t.Context(), WithBudget(time.Second))
	defer table.Close()
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	table.OnDone(func(_ string, _ State, _ string, err error) { done <- err })
	_, err := table.Start(t.Context(), Spec{RuntimeBudget: 30 * time.Millisecond, Run: func(context.Context) (string, error) { <-release; return "late", nil }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "30ms") {
			t.Fatalf("budget result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pinned budget not enforced")
	}
	if table.Active() {
		t.Fatal("timed out work retained local slot")
	}
}

func TestQueueBoundAndChangedConcurrencyPreserveAdmittedWork(t *testing.T) {
	table := NewTable(t.Context())
	defer table.Close()
	table.SetWorkerLimits(1, 1)
	release := make(chan struct{})
	defer close(release)
	starts := make(chan struct{}, 3)
	spec := Spec{Run: func(context.Context) (string, error) { starts <- struct{}{}; <-release; return "done", nil }}
	if _, err := table.Start(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	<-starts
	if _, err := table.Start(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Start(t.Context(), spec); err == nil {
		t.Fatal("queue bound allowed extra work")
	}
	table.SetWorkerLimits(2, 1)
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("new concurrency didn't schedule queued work")
	}
	table.SetWorkerLimits(1, 1)
	if len(table.Status()) != 2 {
		t.Fatal("lowering concurrency lost jobs")
	}
}
