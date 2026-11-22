package job

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// exitRecorder records done and exit callbacks in the order they fired.
type exitRecorder struct {
	mu     sync.Mutex
	order  []string
	exited chan string
}

func watchExits(t *Table) *exitRecorder {
	r := &exitRecorder{exited: make(chan string, 64)}
	t.OnDone(func(id string, state State, _ string, _ error) {
		r.mu.Lock()
		r.order = append(r.order, "done:"+id+":"+string(state))
		r.mu.Unlock()
	})
	t.OnExit(func(id string) {
		r.mu.Lock()
		r.order = append(r.order, "exit:"+id)
		r.mu.Unlock()
		r.exited <- id
	})
	return r
}

func (r *exitRecorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func (r *exitRecorder) awaitExit(t *testing.T, want string) {
	t.Helper()
	select {
	case id := <-r.exited:
		if id != want {
			t.Fatalf("exit callback for %s, want %s", id, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no exit callback for %s", want)
	}
}

func infoOf(t *testing.T, table *Table, id string) Info {
	t.Helper()
	for _, info := range table.Status() {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("no job %s", id)
	return Info{}
}

func exitedNow(table *Table, ids ...string) bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return table.AwaitExit(ctx, ids...) == nil
}

func awaitExited(t *testing.T, table *Table, ids ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := table.AwaitExit(ctx, ids...); err != nil {
		t.Fatalf("AwaitExit(%v) = %v", ids, err)
	}
}

func startBlocking(t *testing.T, table *Table, task string, run func(context.Context) (string, error)) string {
	t.Helper()
	id, err := table.Start(context.Background(), Spec{Task: task, Run: run})
	if err != nil {
		t.Fatalf("Start(%s): %v", task, err)
	}
	return id
}

func TestCancelQueuedJobCountsAsExitedAtOnce(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	started := make(chan struct{})
	running := startBlocking(t, table, "running", func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started
	queued := startBlocking(t, table, "queued", func(context.Context) (string, error) {
		t.Error("the cancelled queued job ran")
		return "", nil
	})

	before, ok := table.Cancel(queued)
	if !ok || before.State != StateQueued {
		t.Fatalf("Cancel(queued) = %+v, %v; want it found queued", before, ok)
	}
	if info := infoOf(t, table, queued); info.State != StateAborted || info.Stopping {
		t.Fatalf("queued job after cancel = %+v, want aborted and not stopping", info)
	}
	if !exitedNow(table, queued) {
		t.Fatal("a job that never ran is not waited on")
	}
	table.Abort(running)
	rec.awaitExit(t, running)
	for _, e := range rec.events() {
		if e == "exit:"+queued {
			t.Fatal("a job that never ran fired the exit callback")
		}
	}
}

func TestCancelRunningJobIsStoppingUntilItsRunReturns(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	started, release := make(chan struct{}), make(chan struct{})
	id := startBlocking(t, table, "slow to stop", func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	})
	<-started

	before, ok := table.Cancel(id)
	if !ok || before.State != StateRunning {
		t.Fatalf("Cancel = %+v, %v; want it found running", before, ok)
	}
	info := infoOf(t, table, id)
	if info.State != StateAborted || !info.Stopping {
		t.Fatalf("after cancel = %+v, want aborted and stopping", info)
	}
	if table.Active() {
		t.Fatal("a stopping job still counts as active")
	}
	if exitedNow(table, id) {
		t.Fatal("AwaitExit returned before the run function did")
	}

	close(release)
	rec.awaitExit(t, id)
	if info := infoOf(t, table, id); info.Stopping || info.State != StateAborted {
		t.Fatalf("after the run returned = %+v, want aborted and no longer stopping", info)
	}
	awaitExited(t, table, id)
	if got := rec.events(); len(got) != 2 || got[0] != "done:"+id+":aborted" || got[1] != "exit:"+id {
		t.Fatalf("callbacks = %v, want the done callback then the exit", got)
	}
}

// A run function that never looks at its context stays stopping for as long
// as it runs, and the job behind it starts at once regardless.
func TestWorkerIgnoringCancelStaysStoppingAndFreesItsSlot(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	started, release := make(chan struct{}), make(chan struct{})
	stubborn := startBlocking(t, table, "stubborn", func(context.Context) (string, error) {
		close(started)
		<-release
		return "late", nil
	})
	<-started
	nextRan := make(chan struct{})
	next := startBlocking(t, table, "next", func(context.Context) (string, error) {
		close(nextRan)
		return "ok", nil
	})

	table.Abort(stubborn)
	select {
	case <-nextRan:
	case <-time.After(2 * time.Second):
		t.Fatal("the stopping job held the queue")
	}
	awaitExited(t, table, next)
	if info := infoOf(t, table, stubborn); !info.Stopping || info.Result != "" {
		t.Fatalf("stubborn job = %+v, want still stopping with no result", info)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := table.AwaitExit(ctx, stubborn); err == nil {
		t.Fatal("AwaitExit gave up waiting without saying so")
	}

	close(release)
	rec.awaitExit(t, stubborn)
	if info := infoOf(t, table, stubborn); info.Stopping || info.State != StateAborted || info.Result != "" {
		t.Fatalf("after the late return = %+v, want aborted with the late result dropped", info)
	}
}

func TestPanicAfterCancelIsTheObservedExit(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	started, release := make(chan struct{}), make(chan struct{})
	id := startBlocking(t, table, "panics on the way out", func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		<-release
		panic("boom")
	})
	<-started
	table.Abort(id)
	close(release)
	rec.awaitExit(t, id)
	if info := infoOf(t, table, id); info.State != StateAborted || info.Stopping {
		t.Fatalf("after the panic = %+v, want it still aborted and no longer stopping", info)
	}
}

func TestPanicWithoutCancelFailsAndIsNotStopping(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	id := startBlocking(t, table, "panics", func(context.Context) (string, error) { panic("boom") })
	awaitExited(t, table, id)
	if info := infoOf(t, table, id); info.State != StateFailed || info.Stopping {
		t.Fatalf("panicked job = %+v, want failed and not stopping", info)
	}
	for _, e := range rec.events() {
		if e == "exit:"+id {
			t.Fatal("a job that ended on its own fired the exit callback")
		}
	}
}

// Whichever of a finishing run and a cancel commits first decides the
// outcome; the loser changes nothing, and the job never stays stopping.
func TestCompletionRacingCancelSettlesOnce(t *testing.T) {
	t.Parallel()
	for range 200 {
		table := NewTable(context.Background())
		rec := watchExits(table)
		started, gate := make(chan struct{}), make(chan struct{})
		id := startBlocking(t, table, "racing", func(context.Context) (string, error) {
			close(started)
			<-gate
			return "finished", nil
		})
		<-started
		var before Info
		var wg sync.WaitGroup
		wg.Go(func() { before, _ = table.Cancel(id) })
		close(gate)
		wg.Wait()
		awaitExited(t, table, id)

		// The run's return is recorded before its own done callback runs, and
		// an exit callback fires just after; give both that moment.
		info := infoOf(t, table, id)
		for deadline := time.Now().Add(2 * time.Second); (info.Stopping || len(rec.events()) == 0) && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
			info = infoOf(t, table, id)
		}
		if info.Stopping {
			t.Fatalf("job left stopping: %+v", info)
		}
		switch info.State {
		case StateDone:
			if before.State != StateDone || info.Result != "finished" {
				t.Fatalf("finished first, but cancel saw %q and result is %q", before.State, info.Result)
			}
		case StateAborted:
			if before.State != StateRunning || info.Result != "" {
				t.Fatalf("cancelled first, but cancel saw %q and result is %q", before.State, info.Result)
			}
		default:
			t.Fatalf("state = %q", info.State)
		}
		dones, exits := 0, 0
		for _, e := range rec.events() {
			switch {
			case strings.HasPrefix(e, "done:"):
				dones++
			case strings.HasPrefix(e, "exit:"):
				exits++
			}
		}
		if dones != 1 || exits > 1 || (exits == 1 && info.State != StateAborted) {
			t.Fatalf("callbacks = %v, want one done and at most one exit, only after a cancel", rec.events())
		}
	}
}

func TestCancelDuringStartAnnouncementNeverRunsAndIsNotStopping(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	rec := watchExits(table)
	inStart, hold := make(chan struct{}), make(chan struct{})
	table.OnStart(func(string, string) {
		close(inStart)
		<-hold
	})
	id := startBlocking(t, table, "announcing", func(context.Context) (string, error) {
		t.Error("run function invoked for a job cancelled while announcing")
		return "", nil
	})
	<-inStart
	before, ok := table.Cancel(id)
	if !ok || before.State != StateRunning {
		t.Fatalf("Cancel = %+v, %v; want it found running", before, ok)
	}
	if exitedNow(table, id) {
		t.Fatal("the job's goroutine is still in its start callback")
	}
	close(hold)
	awaitExited(t, table, id)
	if info := infoOf(t, table, id); info.State != StateAborted || info.Stopping {
		t.Fatalf("after the announcement = %+v, want aborted and not stopping", info)
	}
	for _, e := range rec.events() {
		if e == "exit:"+id {
			t.Fatal("a run function that never ran fired the exit callback")
		}
	}
}

// Every job a cancel-all ends is terminal before the first done callback
// runs, so no slot freed by one of them can start a queued job.
func TestCancelAllStartsNoQueuedJobInBetween(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithMaxConcurrent(2))
	var mu sync.Mutex
	var startedIDs []string
	var leftover []Info
	firstDone := true
	table.OnStart(func(id, _ string) {
		mu.Lock()
		startedIDs = append(startedIDs, id)
		mu.Unlock()
	})
	table.OnDone(func(string, State, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if !firstDone {
			return
		}
		firstDone = false
		for _, info := range table.Status() {
			if info.State == StateQueued || info.State == StateRunning {
				leftover = append(leftover, info)
			}
		}
	})
	var ready sync.WaitGroup
	ready.Add(2)
	var running []string
	for _, task := range []string{"first", "second"} {
		running = append(running, startBlocking(t, table, task, func(ctx context.Context) (string, error) {
			ready.Done()
			<-ctx.Done()
			return "", ctx.Err()
		}))
	}
	ready.Wait()
	var queued []string
	for range 10 {
		queued = append(queued, startBlocking(t, table, "queued", func(context.Context) (string, error) {
			t.Error("a queued job ran during cancel-all")
			return "", nil
		}))
	}

	ended := table.CancelAll(nil)
	if len(ended) != 12 || ended[0].State != StateRunning || ended[1].State != StateRunning || ended[2].State != StateQueued {
		t.Fatalf("CancelAll reported %+v, want two running then ten queued", ended)
	}
	awaitExited(t, table, append(running, queued...)...)
	mu.Lock()
	defer mu.Unlock()
	if len(leftover) != 0 {
		t.Fatalf("jobs still live when the first done callback ran: %+v", leftover)
	}
	if len(startedIDs) != 2 {
		t.Fatalf("start callbacks = %v, want only the two running jobs", startedIDs)
	}
	if table.Active() {
		t.Fatal("jobs still active after cancel-all")
	}
}

func TestCancelAllLeavesSkippedJobsQueued(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	started := make(chan struct{})
	running := startBlocking(t, table, "running", func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started
	keptRan := make(chan struct{})
	kept := startBlocking(t, table, "kept", func(context.Context) (string, error) {
		close(keptRan)
		return "ok", nil
	})
	dropped := startBlocking(t, table, "dropped", func(context.Context) (string, error) {
		t.Error("a cancelled queued job ran")
		return "", nil
	})

	ended := table.CancelAll(func(id string) bool { return id == kept })
	if len(ended) != 2 || ended[0].ID != running || ended[1].ID != dropped {
		t.Fatalf("CancelAll ended %+v, want the running job and the unskipped queued one", ended)
	}
	select {
	case <-keptRan:
	case <-time.After(2 * time.Second):
		t.Fatal("the skipped job never started")
	}
}
