package job

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParentContextTimeoutDoesNotAbortJob(t *testing.T) {
	t.Parallel()
	lifetime := context.Background()
	table := NewTable(lifetime)

	parent, parentCancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	done := make(chan struct{})

	table.OnDone(func(id string, state State, result string, err error) {
		if state != StateDone {
			t.Errorf("state = %q, want done", state)
		}
		close(done)
	})

	id, err := table.Start(parent, Spec{
		Task: "slow",
		Run: func(ctx context.Context) (string, error) {
			ran.Store(true)
			select {
			case <-time.After(80 * time.Millisecond):
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	parentCancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish after parent cancel")
	}
	if !ran.Load() {
		t.Fatal("job run function never executed")
	}
	st := table.Status()
	if len(st) != 1 || st[0].ID != id || st[0].State != StateDone {
		t.Fatalf("status = %+v, want done %s", st, id)
	}
}

func TestAbortCancelsRunningJob(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())

	started := make(chan struct{})
	done := make(chan State, 1)

	table.OnDone(func(_ string, state State, _ string, _ error) {
		done <- state
	})

	id, err := table.Start(context.Background(), Spec{
		Task: "work",
		Run: func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-started
	table.Abort(id)

	select {
	case state := <-done:
		if state != StateAborted {
			t.Fatalf("terminal state = %q, want aborted", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not abort")
	}
}

func TestAbortMissingIDIsNoOp(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	table.Abort("missing")
}

func TestStartAfterCloseErrors(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	table.Close()
	if _, err := table.Start(context.Background(), Spec{Task: "x"}); err == nil {
		t.Fatal("Start after Close = nil, want error")
	}
}

func TestSecondSpawnQueues(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())

	block := make(chan struct{})
	firstStarted := make(chan struct{})
	var orderMu sync.Mutex
	var order []string

	table.OnDone(func(id string, state State, _ string, _ error) {
		if state == StateDone {
			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()
		}
	})

	id1, err := table.Start(context.Background(), Spec{
		Task: "first",
		Run: func(ctx context.Context) (string, error) {
			close(firstStarted)
			<-block
			return "one", nil
		},
	})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-firstStarted

	id2, err := table.Start(context.Background(), Spec{Task: "second", Run: func(ctx context.Context) (string, error) {
		return "two", nil
	}})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}

	st := table.Status()
	var queued int
	for _, info := range st {
		if info.State == StateQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("queued jobs = %d, want 1; status=%+v", queued, st)
	}

	close(block)
	deadline := time.After(3 * time.Second)
	for {
		orderMu.Lock()
		n := len(order)
		orderMu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			orderMu.Lock()
			got := append([]string(nil), order...)
			orderMu.Unlock()
			t.Fatalf("finished order = %v, want both jobs", got)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	orderMu.Lock()
	got := append([]string(nil), order...)
	orderMu.Unlock()
	if got[0] != id1 || got[1] != id2 {
		t.Fatalf("finish order = %v, want %s then %s", got, id1, id2)
	}
}

func TestFailedJobState(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	done := make(chan State, 1)
	table.OnDone(func(_ string, state State, _ string, err error) {
		if err == nil {
			t.Error("want error on failed job")
		}
		done <- state
	})

	_, err := table.Start(context.Background(), Spec{
		Task: "fail",
		Run: func(context.Context) (string, error) {
			return "", errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case state := <-done:
		if state != StateFailed {
			t.Fatalf("state = %q, want failed", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
}

func TestIDFromContextMissingIsEmpty(t *testing.T) {
	t.Parallel()
	if got := IDFromContext(context.Background()); got != "" {
		t.Fatalf("IDFromContext(background) = %q, want empty", got)
	}
}

func TestRunContextCarriesJobIDAndStart(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())

	seen := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	table.OnDone(func(string, State, string, error) { close(done) })

	id, err := table.Start(context.Background(), Spec{
		Task: "look around",
		Run: func(ctx context.Context) (string, error) {
			seen <- IDFromContext(ctx)
			<-release
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case got := <-seen:
		if got != id {
			t.Fatalf("IDFromContext inside Run = %q, want %q", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job never ran")
	}

	st := table.Status()
	if len(st) != 1 || st[0].Started.IsZero() {
		t.Fatalf("running job Info = %+v, want a Started stamp", st)
	}
	if st[0].Elapsed() <= 0 {
		t.Fatalf("Elapsed() = %v on a running job, want positive", st[0].Elapsed())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
}

func TestElapsedIsZeroBeforeStart(t *testing.T) {
	t.Parallel()
	if got := (Info{ID: "job-1"}).Elapsed(); got != 0 {
		t.Fatalf("Elapsed() = %v on a never-started job, want 0", got)
	}
}

func TestOnStartPrecedesRun(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 4)
	done := make(chan struct{})
	table.OnStart(func(id, task string) { order <- "start:" + id + ":" + task })
	table.OnDone(func(string, State, string, error) { close(done) })

	id, err := table.Start(context.Background(), Spec{
		Task: "quick",
		Run: func(ctx context.Context) (string, error) {
			order <- "run:" + IDFromContext(ctx)
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
	close(order)
	got := drainOrder(order)
	want := "start:" + id + ":quick,run:" + id
	if got != want {
		t.Fatalf("order = %q, want %q: the start callback must run before the job can", got, want)
	}
}

// A Start issued from inside another job's start callback must not let the
// pump launch the outer job before its callback has returned, and FIFO holds.
func TestStartFromWithinOnStartKeepsOrder(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 8)
	finished := make(chan struct{}, 2)
	table.OnDone(func(string, State, string, error) { finished <- struct{}{} })
	run := func(ctx context.Context) (string, error) {
		order <- "run:" + IDFromContext(ctx)
		return "ok", nil
	}
	table.OnStart(func(id, task string) {
		order <- "start:" + id
		if task != "outer" {
			return
		}
		if _, err := table.Start(context.Background(), Spec{Task: "inner", Run: run}); err != nil {
			t.Errorf("nested Start: %v", err)
		}
	})

	if _, err := table.Start(context.Background(), Spec{Task: "outer", Run: run}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("jobs did not finish")
		}
	}
	close(order)
	got := drainOrder(order)
	want := "start:job-1,start:job-2,run:job-1,run:job-2"
	if got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func drainOrder(ch <-chan string) string {
	out := ""
	for s := range ch {
		if out != "" {
			out += ","
		}
		out += s
	}
	return out
}

// The next queued job must not run until the previous job's done callback has
// returned; otherwise its events can interleave with the earlier terminal one.
func TestDoneCallbackCompletesBeforeNextJobRuns(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 8)
	release := make(chan struct{})
	table.OnDone(func(id string, _ State, _ string, _ error) {
		order <- "done:" + id
		if id == "job-1" {
			<-release
		}
	})
	run := func(ctx context.Context) (string, error) {
		order <- "run:" + IDFromContext(ctx)
		return "ok", nil
	}
	for _, task := range []string{"first", "second"} {
		if _, err := table.Start(context.Background(), Spec{Task: task, Run: run}); err != nil {
			t.Fatalf("Start(%s): %v", task, err)
		}
	}

	next := func(want string) {
		t.Helper()
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	next("run:job-1")
	next("done:job-1")
	select {
	case got := <-order:
		t.Fatalf("job-2 produced %q while job-1's done callback was still running", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	next("run:job-2")
	next("done:job-2")
}
