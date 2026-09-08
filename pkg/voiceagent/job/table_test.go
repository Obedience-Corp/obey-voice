package job

import (
	"context"
	"errors"
	"strings"
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

func TestQueuedAndStartPrecedeRun(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 4)
	done := make(chan struct{})
	table.OnQueued(func(id, task string) { order <- "queued:" + id + ":" + task })
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
	want := "queued:" + id + ":quick,start:" + id + ":quick,run:" + id
	if got != want {
		t.Fatalf("order = %q, want %q: queued, then start, then the job itself", got, want)
	}
}

// A Start issued from inside another job's queued callback must not let the
// pump launch the outer job before its callback has returned, and FIFO holds.
func TestStartFromWithinOnQueuedKeepsOrder(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 8)
	finished := make(chan struct{}, 2)
	table.OnDone(func(string, State, string, error) { finished <- struct{}{} })
	run := func(ctx context.Context) (string, error) {
		order <- "run:" + IDFromContext(ctx)
		return "ok", nil
	}
	table.OnStart(func(id, _ string) { order <- "start:" + id })
	table.OnQueued(func(id, task string) {
		order <- "queued:" + id
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
	want := "queued:job-1,queued:job-2,start:job-1,run:job-1,start:job-2,run:job-2"
	if got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

// awaitOrder pulls the next entry off an order channel and fails on anything
// but want.
func awaitOrder(t *testing.T, order <-chan string, want string) {
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

// A job queued behind a running one is announced as queued at once, but its
// start callback fires only after the previous job's done callback returned,
// and still before its own run function.
func TestQueuedJobStartsAfterPreviousDone(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 16)
	release := make(chan struct{})
	table.OnQueued(func(id, _ string) { order <- "queued:" + id })
	table.OnStart(func(id, _ string) { order <- "start:" + id })
	table.OnDone(func(id string, _ State, _ string, _ error) { order <- "done:" + id })
	hold := func(ctx context.Context) (string, error) {
		order <- "run:" + IDFromContext(ctx)
		<-release
		return "ok", nil
	}
	quick := func(ctx context.Context) (string, error) {
		order <- "run:" + IDFromContext(ctx)
		return "ok", nil
	}

	if _, err := table.Start(context.Background(), Spec{Task: "first", Run: hold}); err != nil {
		t.Fatalf("Start(first): %v", err)
	}
	awaitOrder(t, order, "queued:job-1")
	awaitOrder(t, order, "start:job-1")
	awaitOrder(t, order, "run:job-1")

	if _, err := table.Start(context.Background(), Spec{Task: "second", Run: quick}); err != nil {
		t.Fatalf("Start(second): %v", err)
	}
	awaitOrder(t, order, "queued:job-2")
	select {
	case got := <-order:
		t.Fatalf("got %q while job-1 was still running, want job-2 held in the queue", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	awaitOrder(t, order, "done:job-1")
	awaitOrder(t, order, "start:job-2")
	awaitOrder(t, order, "run:job-2")
	awaitOrder(t, order, "done:job-2")
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

	awaitOrder(t, order, "run:job-1")
	awaitOrder(t, order, "done:job-1")
	select {
	case got := <-order:
		t.Fatalf("job-2 produced %q while job-1's done callback was still running", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	awaitOrder(t, order, "run:job-2")
	awaitOrder(t, order, "done:job-2")
}

// With room for two, two jobs run at once and the third waits; Status lists
// the running ones first, in start order.
func TestMaxConcurrentRunsJobsTogether(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithMaxConcurrent(2))
	started := make(chan string, 3)
	release := make(chan struct{})
	hold := func(ctx context.Context) (string, error) {
		started <- IDFromContext(ctx)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	for _, task := range []string{"a", "b", "c"} {
		if _, err := table.Start(context.Background(), Spec{Task: task, Run: hold}); err != nil {
			t.Fatalf("Start(%s): %v", task, err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d job(s) ran, want 2 at once", i)
		}
	}
	select {
	case id := <-started:
		t.Fatalf("%s ran beyond the limit of 2", id)
	case <-time.After(50 * time.Millisecond):
	}

	st := table.Status()
	if len(st) != 3 || st[0].ID != "job-1" || st[1].ID != "job-2" || st[2].ID != "job-3" {
		t.Fatalf("status order = %+v, want job-1, job-2 running then job-3", st)
	}
	if st[0].State != StateRunning || st[1].State != StateRunning || st[2].State != StateQueued {
		t.Fatalf("states = %s/%s/%s, want running/running/queued", st[0].State, st[1].State, st[2].State)
	}

	close(release)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the queued job never ran once room opened")
	}
}

// A job that outlives the budget is stopped with an error naming the budget,
// which is how consumers tell it from a user's cancel.
func TestBudgetStopsARunningJob(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithBudget(40*time.Millisecond))
	type outcome struct {
		state State
		err   error
	}
	done := make(chan outcome, 1)
	table.OnDone(func(_ string, state State, _ string, err error) { done <- outcome{state, err} })

	id, err := table.Start(context.Background(), Spec{
		Task: "endless",
		Run: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case got := <-done:
		if got.state != StateAborted {
			t.Fatalf("state = %q, want aborted", got.state)
		}
		if !errors.Is(got.err, ErrBudget) {
			t.Fatalf("err = %v, want ErrBudget", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("budget never stopped the job")
	}
	st := table.Status()
	if len(st) != 1 || st[0].ID != id || st[0].State != StateAborted || !strings.Contains(st[0].Err, "budget") {
		t.Fatalf("status = %+v, want aborted with a budget error", st)
	}
}

// A job that finishes inside its budget must not be touched when the timer
// would have fired.
func TestBudgetTimerDoesNotOutliveAFinishedJob(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithBudget(30*time.Millisecond))
	var calls atomic.Int32
	done := make(chan State, 2)
	table.OnDone(func(_ string, state State, _ string, _ error) {
		calls.Add(1)
		done <- state
	})
	if _, err := table.Start(context.Background(), Spec{Task: "quick", Run: func(context.Context) (string, error) {
		return "ok", nil
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case state := <-done:
		if state != StateDone {
			t.Fatalf("state = %q, want done", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
	time.Sleep(80 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("done callbacks = %d, want exactly 1", n)
	}
	if st := table.Status(); st[0].State != StateDone || st[0].Err != "" {
		t.Fatalf("status = %+v, want done and untouched by the budget timer", st)
	}
}

// Status must name the running job before the one queued behind it, or a
// status query answers with the wrong job.
func TestStatusListsRunningBeforeQueued(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	ran := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if _, err := table.Start(context.Background(), Spec{Task: "first", Run: func(ctx context.Context) (string, error) {
		close(ran)
		<-release
		return "ok", nil
	}}); err != nil {
		t.Fatalf("Start(first): %v", err)
	}
	<-ran
	if _, err := table.Start(context.Background(), Spec{Task: "second", Run: func(context.Context) (string, error) {
		return "ok", nil
	}}); err != nil {
		t.Fatalf("Start(second): %v", err)
	}
	st := table.Status()
	if len(st) != 2 || st[0].State != StateRunning || st[1].State != StateQueued {
		t.Fatalf("status = %+v, want the running job first, then the queued one", st)
	}
}

// The budget is the table's promise, not the worker's: a run function that
// ignores its cancel is still ended at the budget, frees its slot, and its
// late return is ignored.
func TestBudgetEndsAJobThatIgnoresItsCancel(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithBudget(40*time.Millisecond))
	var callbacks atomic.Int32
	done := make(chan error, 4)
	table.OnDone(func(_ string, _ State, _ string, err error) {
		callbacks.Add(1)
		done <- err
	})
	ran := make(chan struct{})
	release := make(chan struct{})
	if _, err := table.Start(context.Background(), Spec{Task: "stubborn", Run: func(context.Context) (string, error) {
		close(ran)
		<-release // never looks at ctx
		return "late", nil
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-ran

	select {
	case err := <-done:
		if !errors.Is(err, ErrBudget) {
			t.Fatalf("err = %v, want ErrBudget", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the budget did not end the job while its run function ignored the cancel")
	}
	if table.Active() {
		t.Fatal("the stopped job still holds its slot")
	}

	second := make(chan struct{})
	if _, err := table.Start(context.Background(), Spec{Task: "next", Run: func(context.Context) (string, error) {
		close(second)
		return "ok", nil
	}}); err != nil {
		t.Fatalf("Start(next): %v", err)
	}
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("the slot was not freed for the next job")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the next job did not finish")
	}

	close(release) // the stubborn run function finally returns
	time.Sleep(40 * time.Millisecond)
	if n := callbacks.Load(); n != 2 {
		t.Fatalf("done callbacks = %d, want 2: the late return must not fire a third", n)
	}
	st := table.Status()
	for _, info := range st {
		if info.ID == "job-1" && (info.State != StateAborted || info.Result != "" || !strings.Contains(info.Err, "budget")) {
			t.Fatalf("job-1 = %+v, want aborted by the budget with no result from the late return", info)
		}
	}
}

// A cancel ends the job the same way: at once, slot freed, late return ignored.
func TestAbortEndsARunningJobWithoutWaiting(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	done := make(chan State, 2)
	table.OnDone(func(_ string, state State, _ string, _ error) { done <- state })
	ran := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	id, err := table.Start(context.Background(), Spec{Task: "stubborn", Run: func(context.Context) (string, error) {
		close(ran)
		<-release
		return "late", nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-ran
	table.Abort(id)
	select {
	case state := <-done:
		if state != StateAborted {
			t.Fatalf("state = %q, want aborted", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Abort waited on a run function that ignores its cancel")
	}
	if table.Active() {
		t.Fatal("the aborted job still holds its slot")
	}
}

// A cancel that lands while the job is announcing itself returns at once
// and is applied after the announcement: the start callback precedes the
// done callback, and the run function is never invoked for a job that is
// already over.
func TestAbortDuringStartKeepsOrderAndSkipsRun(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 8)
	hold := make(chan struct{})
	inStart := make(chan struct{})
	table.OnStart(func(id, _ string) {
		close(inStart)
		<-hold
		order <- "start:" + id
	})
	table.OnDone(func(id string, state State, _ string, _ error) { order <- "done:" + id + ":" + string(state) })
	var ran atomic.Bool
	id, err := table.Start(context.Background(), Spec{Task: "x", Run: func(context.Context) (string, error) {
		ran.Store(true)
		return "ok", nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-inStart

	aborted := make(chan struct{})
	go func() {
		table.Abort(id)
		close(aborted)
	}()
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort blocked on the start callback")
	}
	select {
	case got := <-order:
		t.Fatalf("got %q before the start callback returned", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(hold)
	awaitOrder(t, order, "start:"+id)
	awaitOrder(t, order, "done:"+id+":aborted")
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("the run function was invoked for a job stopped during its start")
	}
	if table.Active() {
		t.Fatal("the stopped job is still active")
	}
}

// The budget timer is armed before the start callback, so it can fire during
// the announcement; that stop takes the same non-blocking path as a cancel.
// The timer is fired by hand from inside the callback so the test does not
// depend on scheduling.
func TestBudgetFiringDuringStartKeepsOrder(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background(), WithBudget(time.Hour))
	order := make(chan string, 8)
	table.OnStart(func(id, _ string) {
		table.exceedBudget(id)
		order <- "start:" + id
	})
	table.OnDone(func(id string, state State, _ string, err error) {
		if !errors.Is(err, ErrBudget) {
			t.Errorf("done err = %v, want ErrBudget", err)
		}
		order <- "done:" + id + ":" + string(state)
	})
	var ran atomic.Bool
	id, err := table.Start(context.Background(), Spec{Task: "x", Run: func(context.Context) (string, error) {
		ran.Store(true)
		return "ok", nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	awaitOrder(t, order, "start:"+id)
	awaitOrder(t, order, "done:"+id+":aborted")
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("the run function was invoked for a job the budget ended during its start")
	}
	if table.Active() {
		t.Fatal("the stopped job is still active")
	}
}

// A start callback may abort its own job: Abort never waits on the callback
// that is calling it, and the job still ends in order with no run.
func TestStartCallbackCanAbortItsOwnJob(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	order := make(chan string, 8)
	table.OnStart(func(id, _ string) {
		order <- "start:" + id
		table.Abort(id)
		order <- "abort-returned:" + id
	})
	table.OnDone(func(id string, state State, _ string, _ error) { order <- "done:" + id + ":" + string(state) })
	var ran atomic.Bool
	id, err := table.Start(context.Background(), Spec{Task: "x", Run: func(context.Context) (string, error) {
		ran.Store(true)
		return "ok", nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	awaitOrder(t, order, "start:"+id)
	awaitOrder(t, order, "abort-returned:"+id)
	awaitOrder(t, order, "done:"+id+":aborted")
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("the run function was invoked for a job its own start callback aborted")
	}
	if table.Active() {
		t.Fatal("the aborted job is still active")
	}

	// The table keeps working afterwards.
	next := make(chan struct{})
	table.OnStart(nil)
	if _, err := table.Start(context.Background(), Spec{Task: "next", Run: func(context.Context) (string, error) {
		close(next)
		return "ok", nil
	}}); err != nil {
		t.Fatalf("Start(next): %v", err)
	}
	select {
	case <-next:
	case <-time.After(2 * time.Second):
		t.Fatal("the next job never ran")
	}
}
