package job

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type scheduleHarness struct {
	t       *testing.T
	table   *Table
	started chan string
	release map[string]chan struct{}
}

func newScheduleHarness(t *testing.T, opts ...Option) *scheduleHarness {
	t.Helper()
	table := NewTable(context.Background(), opts...)
	t.Cleanup(table.AbortAll)
	return &scheduleHarness{t: t, table: table, started: make(chan string, 16), release: map[string]chan struct{}{}}
}

// spec is a job named task that runs until released or cancelled.
func (h *scheduleHarness) spec(task, resource string) Spec {
	release := make(chan struct{})
	h.release[task] = release
	return Spec{Task: task, Resource: resource, Run: func(ctx context.Context) (string, error) {
		h.started <- task
		select {
		case <-release:
			return task, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
}

func (h *scheduleHarness) start(spec Spec) string {
	h.t.Helper()
	id, err := h.table.Start(context.Background(), spec)
	if err != nil {
		h.t.Fatalf("Start(%s): %v", spec.Task, err)
	}
	return id
}

func (h *scheduleHarness) info(task string) Info {
	h.t.Helper()
	for _, info := range h.table.Status() {
		if info.Task == task {
			return info
		}
	}
	h.t.Fatalf("no job %q in %+v", task, h.table.Status())
	return Info{}
}

func (h *scheduleHarness) wantState(task string, state State) Info {
	h.t.Helper()
	info := h.info(task)
	if info.State != state {
		h.t.Fatalf("%s is %s, want %s (%+v)", task, info.State, state, h.table.Status())
	}
	return info
}

func (h *scheduleHarness) wantWaiting(task string, kind WaitKind, reason string) {
	h.t.Helper()
	info := h.wantState(task, StateQueued)
	if info.WaitKind != kind || (reason != "" && info.Waiting != reason) {
		h.t.Fatalf("%s waits for %q %q, want %q %q", task, info.WaitKind, info.Waiting, kind, reason)
	}
}

func (h *scheduleHarness) finish(task string) {
	close(h.release[task])
}

func (h *scheduleHarness) awaitStart(want string) {
	h.t.Helper()
	select {
	case got := <-h.started:
		if got != want {
			h.t.Fatalf("started %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		h.t.Fatalf("%s never started", want)
	}
}

func (h *scheduleHarness) noStart() {
	h.t.Helper()
	select {
	case got := <-h.started:
		h.t.Fatalf("%s started unexpectedly", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestJobsOnDifferentResourcesRunTogether(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", "local-model-a"))
	h.start(h.spec("second", "local-model-b"))
	h.wantState("first", StateRunning)
	info := h.wantState("second", StateRunning)
	if info.WaitKind != "" || info.Waiting != "" {
		t.Fatalf("running job still reports waiting: %+v", info)
	}
}

func TestJobsOnOneResourceRunOneAtATimeInOrder(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", "local-model-a"))
	h.awaitStart("first")
	h.start(h.spec("second", "local-model-a"))
	h.start(h.spec("other", "local-model-b"))
	h.start(h.spec("third", "local-model-a"))
	h.awaitStart("other")
	h.wantWaiting("second", WaitResource, "same model busy")
	h.wantWaiting("third", WaitResource, "same model busy")

	h.finish("first")
	h.awaitStart("second")
	h.noStart()
	h.wantWaiting("third", WaitResource, "")
	h.finish("second")
	h.awaitStart("third")
}

func TestJobsWithoutResourceAreNeverSerialized(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(3))
	for _, task := range []string{"one", "two", "three"} {
		h.start(h.spec(task, ""))
	}
	for _, task := range []string{"one", "two", "three"} {
		h.wantState(task, StateRunning)
	}
}

func TestCeilingHoldsJobsAndSaysWhy(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(2))
	h.start(h.spec("one", "local-model-a"))
	h.awaitStart("one")
	h.start(h.spec("two", ""))
	h.awaitStart("two")
	h.start(h.spec("three", "local-model-b"))
	h.wantWaiting("three", WaitCeiling, "2 jobs already running")
	h.noStart()
	h.finish("two")
	h.awaitStart("three")
}

func TestAdmitDefersAJobOnlyBesideRunningWork(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", "local-model-a"))
	var seen []string
	gated := h.spec("gated", "local-model-b")
	gated.Admit = func(running []Info) (bool, string) {
		seen = seen[:0]
		for _, info := range running {
			seen = append(seen, info.Task)
		}
		return false, "waiting for memory (needs about 30 GB, 16 GB free)"
	}
	h.start(gated)
	h.wantWaiting("gated", WaitAdmit, "waiting for memory (needs about 30 GB, 16 GB free)")
	if strings.Join(seen, ",") != "first" {
		t.Fatalf("Admit saw running %v, want [first]", seen)
	}
	// Once nothing else runs, the gate is not asked: the oldest job starts.
	h.awaitStart("first")
	h.finish("first")
	h.awaitStart("gated")
}

func TestOldestJobStartsWhenNothingRunsEvenIfAdmitRefuses(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	refuse := func([]Info) (bool, string) { return false, "waiting for memory" }
	first := h.spec("first", "local-model-a")
	first.Admit = refuse
	second := h.spec("second", "local-model-b")
	second.Admit = refuse
	h.start(first)
	h.wantState("first", StateRunning)
	h.start(second)
	h.wantWaiting("second", WaitAdmit, "waiting for memory")
	h.awaitStart("first")
	h.finish("first")
	h.awaitStart("second")
}

func TestRecheckStartsAJobOnceAdmitAccepts(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", "local-model-a"))
	var fits atomic.Bool
	gated := h.spec("gated", "local-model-b")
	gated.Admit = func([]Info) (bool, string) { return fits.Load(), "waiting for memory" }
	h.start(gated)
	h.awaitStart("first")
	h.table.Recheck()
	h.wantWaiting("gated", WaitAdmit, "")
	fits.Store(true)
	h.table.Recheck()
	h.wantState("gated", StateRunning)
	h.awaitStart("gated")
	h.wantState("first", StateRunning)
}

func TestCancellingAWaitingJobLeavesTheQueue(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", "local-model-a"))
	second := h.start(h.spec("second", "local-model-a"))
	h.start(h.spec("third", "local-model-a"))
	h.awaitStart("first")
	if before, ok := h.table.Cancel(second); !ok || before.State != StateQueued {
		t.Fatalf("Cancel = %+v, %v", before, ok)
	}
	info := h.wantState("second", StateAborted)
	if info.WaitKind != "" || info.Waiting != "" || !info.Started.IsZero() {
		t.Fatalf("cancelled waiting job: %+v", info)
	}
	h.wantWaiting("third", WaitResource, "")
	h.finish("first")
	h.awaitStart("third")
	h.noStart()
}

func TestImmediateJobHoldsItsResource(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("queued first", "local-model-a"))
	h.awaitStart("queued first")
	moved := h.spec("moved", "local-model-a")
	moved.Immediate = true
	h.start(moved)
	h.awaitStart("moved")
	h.finish("queued first")
	h.start(h.spec("later", "local-model-a"))
	h.wantWaiting("later", WaitResource, "same model busy")
	h.noStart()
	h.finish("moved")
	h.awaitStart("later")
}

// A job's resource stays held while its done callback runs, as its slot does.
func TestCompletingJobHoldsItsResource(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	inDone, leaveDone := make(chan struct{}), make(chan struct{})
	h.table.OnDone(func(_ string, _ State, result string, _ error) {
		if result == "first" {
			close(inDone)
			<-leaveDone
		}
	})
	h.start(h.spec("first", "local-model-a"))
	h.start(h.spec("second", "local-model-a"))
	h.awaitStart("first")
	h.finish("first")
	<-inDone
	h.noStart()
	h.wantWaiting("second", WaitResource, "")
	close(leaveDone)
	h.awaitStart("second")
}

func TestQueueLimitRejectsOnlyJobsThatCannotStart(t *testing.T) {
	h := newScheduleHarness(t)
	h.table.SetWorkerLimits(4, 1)
	h.start(h.spec("first", "local-model-a"))
	h.start(h.spec("second", "local-model-a"))
	if _, err := h.table.Start(context.Background(), h.spec("third", "local-model-a")); err == nil || err.Error() != "worker queue limit reached" {
		t.Fatalf("third same-model job = %v, want the queue limit", err)
	}
	h.start(h.spec("other", "local-model-b"))
	h.wantState("other", StateRunning)
}

func TestChangedWorkerLimitsRescheduleWithoutCancelling(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(1))
	h.start(h.spec("one", "local-model-a"))
	h.start(h.spec("two", "local-model-b"))
	h.start(h.spec("three", ""))
	h.wantWaiting("two", WaitCeiling, "1 job already running")
	h.wantWaiting("three", WaitCeiling, "1 job already running")
	h.table.SetWorkerLimits(3, 0)
	h.wantState("two", StateRunning)
	h.wantState("three", StateRunning)
	h.table.SetWorkerLimits(1, 0)
	for _, task := range []string{"one", "two", "three"} {
		h.wantState(task, StateRunning)
	}
	h.start(h.spec("four", "local-model-c"))
	h.wantWaiting("four", WaitCeiling, "3 jobs already running")
}

func TestPanickingAdmitAdmitsTheJob(t *testing.T) {
	h := newScheduleHarness(t, WithMaxConcurrent(4))
	h.start(h.spec("first", ""))
	gated := h.spec("gated", "local-model-b")
	gated.Admit = func([]Info) (bool, string) { panic("probe broke") }
	h.start(gated)
	h.wantState("gated", StateRunning)
}
