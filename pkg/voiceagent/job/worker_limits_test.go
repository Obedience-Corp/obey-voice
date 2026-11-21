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

// Work already under way starts past the limits and holds a slot, so a job
// started after it queues behind it.
func TestImmediateJobStartsPastTheLimitAndHoldsASlot(t *testing.T) {
	table := NewTable(context.Background(), WithMaxConcurrent(1))
	table.SetWorkerLimits(1, 1)
	release := make(chan struct{})
	started := make(chan string, 3)
	run := func(name string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			started <- name
			select {
			case <-release:
				return name, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	for _, spec := range []Spec{{Task: "first", Run: run("first")}, {Task: "queued", Run: run("queued")}} {
		if _, err := table.Start(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-started; got != "first" {
		t.Fatalf("started %q first", got)
	}
	if _, err := table.Start(context.Background(), Spec{Task: "moved", Run: run("moved"), Immediate: true}); err != nil {
		t.Fatalf("immediate job refused with a full queue: %v", err)
	}
	select {
	case got := <-started:
		if got != "moved" {
			t.Fatalf("started %q instead of the immediate job", got)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate job waited for a slot")
	}
	running := 0
	for _, info := range table.Status() {
		if info.State == StateRunning {
			running++
		}
	}
	if running != 2 {
		t.Fatalf("running jobs = %d, want 2: %+v", running, table.Status())
	}
	close(release)
	if got := <-started; got != "queued" {
		t.Fatalf("started %q, want the queued job", got)
	}
}
