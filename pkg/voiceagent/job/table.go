package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrBudget is the error a job ends with when it ran past the table's budget.
// The done callback's err wraps it, so consumers can tell a budget stop from
// a user's cancel with errors.Is.
var ErrBudget = errors.New("job budget exceeded")

// errAborted is the error a job ends with when Abort stopped it.
var errAborted = errors.New("aborted")

// State is a job lifecycle phase.
type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateAborted State = "aborted"
)

// idKey types the job id carried on a job's context.
type idKey struct{}

// WithID returns ctx carrying the job id, so work started by the table can
// attribute what it does back to the job.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, idKey{}, id)
}

// IDFromContext returns the job id ctx carries, or "" outside a job.
func IDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(idKey{}).(string)
	return id
}

// Spec describes work the table should run asynchronously.
type Spec struct {
	// ID optionally supplies a caller-owned stable identity. Empty preserves
	// the table's sequential legacy IDs.
	// RuntimeBudget overrides the table default for this admitted job.
	// Zero keeps the legacy default. It limits local scheduling, not remote termination.
	RuntimeBudget time.Duration
	ID            string
	Task          string
	Run           func(ctx context.Context) (result string, err error)
	// OnAccepted publishes caller metadata after enqueue succeeds, before
	// queued/start callbacks or execution. It runs outside the table lock.
	OnAccepted func(id string)
}

// Info is a point-in-time job snapshot for status UI.
type Info struct {
	ID     string
	Task   string
	State  State
	Result string
	Err    string
	// Started is when the run function began; zero while queued.
	Started time.Time
	// Ended is the monotonic timestamp at which the job became terminal.
	// It is zero while queued or running.
	Ended time.Time
	// TerminalElapsed is frozen when the job ends. Running jobs calculate their
	// current elapsed time from Started instead.
	TerminalElapsed time.Duration
}

// Elapsed reports how long the job has been running, or zero if it never started.
func (i Info) Elapsed() time.Duration {
	if i.Started.IsZero() {
		return 0
	}
	if !i.Ended.IsZero() {
		return i.TerminalElapsed
	}
	return time.Since(i.Started)
}

// DoneCallback fires once per job when it reaches a terminal state.
type DoneCallback func(id string, state State, result string, err error)

// QueuedCallback fires once per job, on the goroutine that called Start and
// before the job can run, so anything it publishes precedes every event the
// job itself produces.
type QueuedCallback func(id, task string)

// StartCallback fires once per job, on the job's own goroutine after it has
// left the queue and immediately before its run function. It therefore
// follows the previous job's done callback and precedes the first thing this
// job does.
type StartCallback func(id, task string)

// Table holds async jobs that outlive one conversational turn.
type Table struct {
	mu       sync.Mutex
	lifetime context.Context
	closed   bool
	nextID   int
	jobs     map[string]*entry
	queue    []string
	// running holds the jobs whose run function is live; runOrder keeps them
	// in the order they started, for status.
	running       map[string]struct{}
	runOrder      []string
	maxConcurrent int
	maxQueued     int
	// budget is the longest a job may run; zero means no limit.
	budget   time.Duration
	onDone   DoneCallback
	onQueued QueuedCallback
	onStart  StartCallback
}

type entry struct {
	info   Info
	spec   Spec
	ctx    context.Context
	cancel context.CancelFunc
	// announced is set once OnQueued has run; the pump never launches a job
	// before that, so its start cannot be published ahead of its queued event.
	announced bool
	// budgetTimer fires the budget stop.
	budgetTimer *time.Timer
	// finished is set once the job reached a terminal state. A stop (cancel
	// or budget) finishes the job without waiting for its run function, so
	// a run function that returns late finds this set and changes nothing.
	finished bool
	// startAnnounced is set by run once the start callback has returned (or
	// was skipped). A stop that lands before that point does not wait — the
	// callback may be the caller — it leaves stopErr, and run applies it
	// right after the announcement: the start event still precedes the
	// terminal one, and the run function is never invoked.
	startAnnounced bool
	stopErr        error
}

// Option configures a Table.
type Option func(*Table)

// WithMaxConcurrent sets how many jobs run at once. Values below one mean one.
func WithMaxConcurrent(n int) Option {
	return func(t *Table) {
		if n < 1 {
			n = 1
		}
		t.maxConcurrent = n
	}
}

// WithBudget caps how long any one job may run. At the budget the job is
// ended by the table: marked aborted with an error wrapping ErrBudget, its
// slot freed and the done callback fired, whether or not the run function
// has returned. The run function is told to stop through its context; one
// that ignores the cancel lingers on its own goroutine but no longer counts.
// Zero or less means no cap.
func WithBudget(d time.Duration) Option {
	return func(t *Table) {
		if d < 0 {
			d = 0
		}
		t.budget = d
	}
}

// NewTable constructs a job table. lifetime scopes jobs past turn cancellation;
// callers typically pass the pipeline or agent context.
func NewTable(lifetime context.Context, opts ...Option) *Table {
	if lifetime == nil {
		lifetime = context.Background()
	}
	t := &Table{
		lifetime:      lifetime,
		jobs:          make(map[string]*entry),
		running:       make(map[string]struct{}),
		maxConcurrent: 1,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// OnDone registers a callback for terminal job states.
func (t *Table) OnDone(cb DoneCallback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onDone = cb
}

// OnQueued registers a callback that runs inside Start, once the job is
// accepted and before it can begin.
func (t *Table) OnQueued(cb QueuedCallback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onQueued = cb
}

// OnStart registers a callback that runs on the job's goroutine right before
// its run function.
func (t *Table) OnStart(cb StartCallback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onStart = cb
}

// Start enqueues work. A spawn beyond the concurrency limit queues in order
// behind the running ones (Q2). parent is ignored for cancellation — jobs use
// context.WithoutCancel(lifetime).
func (t *Table) Start(parent context.Context, spec Spec) (string, error) {
	_ = parent
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", fmt.Errorf("job table closed")
	}
	if t.maxQueued > 0 && len(t.running) >= t.maxConcurrent && len(t.queue) >= t.maxQueued {
		t.mu.Unlock()
		return "", errors.New("worker queue limit reached")
	}
	id := spec.ID
	if id == "" {
		for {
			t.nextID++
			id = fmt.Sprintf("job-%d", t.nextID)
			if _, exists := t.jobs[id]; !exists {
				break
			}
		}
	} else if _, exists := t.jobs[id]; exists {
		t.mu.Unlock()
		return "", fmt.Errorf("job %q already exists", id)
	}
	jobCtx, cancel := context.WithCancel(WithID(context.WithoutCancel(t.lifetime), id))
	e := &entry{
		info:   Info{ID: id, Task: spec.Task, State: StateQueued},
		spec:   spec,
		ctx:    jobCtx,
		cancel: cancel,
	}
	t.jobs[id] = e
	t.queue = append(t.queue, id)
	onQueued := t.onQueued
	t.mu.Unlock()

	// Outside the lock: the callback publishes to the bus, and a subscriber may
	// legitimately ask the table for status.
	if spec.OnAccepted != nil {
		spec.OnAccepted(id)
	}
	if onQueued != nil {
		onQueued(id, spec.Task)
	}

	t.mu.Lock()
	e.announced = true
	t.pumpLocked()
	t.mu.Unlock()
	return id, nil
}

// Abort ends a queued or running job: it is marked aborted, its slot is
// freed, the done callback fires, and the run function is told to stop
// through its context. Abort never blocks, so a start callback may abort its
// own job; a stop that lands while the job is announcing itself is applied
// by the job's goroutine right after the announcement. Missing and finished
// ids are a no-op.
func (t *Table) Abort(id string) {
	t.stop(id, errAborted)
}

// stop is the one way a job ends before its run function returns; user
// cancels and budget breaches share it, so neither waits on a run function
// that may ignore its cancel.
func (t *Table) stop(id string, err error) {
	t.mu.Lock()
	e, ok := t.jobs[id]
	if !ok || e.finished {
		t.mu.Unlock()
		return
	}
	if e.info.State == StateRunning && !e.startAnnounced {
		// Announcing. The caller may be the start callback itself, so never
		// wait here: leave the stop for run to apply once the callback has
		// returned. The first stop to land is the one that counts.
		if e.stopErr == nil {
			e.stopErr = err
		}
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.complete(id, StateAborted, "", err)
}

// complete ends a job: terminal state under the lock, the context cancel,
// the done callback, then a pump once the callback has returned. Every path
// that ends a job goes through it, and a job already ended is left alone.
func (t *Table) complete(id string, state State, result string, err error) {
	t.mu.Lock()
	e, ok := t.jobs[id]
	if !ok || e.finished {
		t.mu.Unlock()
		return
	}
	if e.info.State == StateQueued {
		t.removeFromQueueLocked(id)
	}
	cb := t.finishLocked(id, state, result, err)
	cancel := e.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cb != nil {
		cb(id, state, result, err)
	}
	t.mu.Lock()
	t.pumpLocked()
	t.mu.Unlock()
}

// Status returns all known jobs: running first in start order, then queued in
// queue order, then the rest.
func (t *Table) Status() []Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Info, 0, len(t.jobs))
	for _, id := range t.orderedIDsLocked() {
		if e, ok := t.jobs[id]; ok {
			out = append(out, e.info)
		}
	}
	return out
}

// Active reports whether any job is queued or running.
func (t *Table) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.running) > 0 || len(t.queue) > 0
}

// Close rejects new jobs. It does not cancel running work.
func (t *Table) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
}

// pumpLocked launches queued jobs while there is room. The head of the queue
// is never skipped: if it is not announced yet, its own Start pumps once the
// queued callback has run.
func (t *Table) pumpLocked() {
	for len(t.running) < t.maxConcurrent && len(t.queue) > 0 {
		id := t.queue[0]
		e := t.jobs[id]
		if !e.announced {
			return
		}
		t.queue = t.queue[1:]
		e.info.State = StateRunning
		t.running[id] = struct{}{}
		t.runOrder = append(t.runOrder, id)
		go t.run(e.ctx, id)
	}
}

func (t *Table) run(jobCtx context.Context, id string) {
	defer func() {
		if r := recover(); r != nil {
			t.complete(id, StateFailed, "", fmt.Errorf("worker panic: %v", r))
		}
	}()

	t.mu.Lock()
	e := t.jobs[id]
	onStart := t.onStart
	runFn := e.spec.Run
	if runFn != nil {
		e.info.Started = time.Now()
		budget := t.budget
		if e.spec.RuntimeBudget > 0 {
			budget = e.spec.RuntimeBudget
		}
		if budget > 0 {
			e.budgetTimer = time.AfterFunc(budget, func() { t.exceedBudget(id) })
		}
	}
	t.mu.Unlock()

	// Same goroutine as the run function: whatever the callback publishes is
	// out before the job's first tool call can be.
	if runFn != nil && onStart != nil {
		onStart(id, e.spec.Task)
	}

	t.mu.Lock()
	e.startAnnounced = true
	pending := e.stopErr
	t.mu.Unlock()
	if pending != nil {
		// A stop landed during the announcement (possibly from the callback
		// itself). Apply it here: the start event is out, the run function
		// never begins.
		t.complete(id, StateAborted, "", pending)
		return
	}
	if runFn == nil {
		t.complete(id, StateFailed, "", fmt.Errorf("job %s: no run function", id))
		return
	}
	// A stop that landed after the announcement has already ended the job:
	// the run function is never invoked for it. One landing after this
	// check hands the run function an already-cancelled context instead.
	t.mu.Lock()
	finished := e.finished
	t.mu.Unlock()
	if finished {
		return
	}

	result, err := runFn(jobCtx)
	state := StateDone
	if err != nil {
		if jobCtx.Err() != nil {
			state = StateAborted
		} else {
			state = StateFailed
		}
	}
	// A no-op when a stop ended the job while the run function ignored its
	// cancel: the late return changes nothing.
	t.complete(id, state, result, err)
}

// finishLocked marks a job terminal. Caller must hold t.mu. It returns the
// done callback to invoke after releasing the lock; the caller pumps the queue
// only after that callback has returned.
func (t *Table) finishLocked(id string, state State, result string, err error) DoneCallback {
	e, ok := t.jobs[id]
	if !ok {
		return nil
	}
	e.finished = true
	e.info.State = state
	e.info.Result = result
	e.info.Ended = time.Now()
	if !e.info.Started.IsZero() {
		e.info.TerminalElapsed = e.info.Ended.Sub(e.info.Started)
	}
	if err != nil {
		e.info.Err = err.Error()
	}
	if e.budgetTimer != nil {
		e.budgetTimer.Stop()
	}
	delete(t.running, id)
	for i, running := range t.runOrder {
		if running == id {
			t.runOrder = append(t.runOrder[:i], t.runOrder[i+1:]...)
			break
		}
	}
	return t.onDone
}

// exceedBudget ends a job whose budget timer fired. A job that finished in
// time has stopped its timer under the lock, and stop ignores a finished id,
// so the race between the two is harmless.
func (t *Table) exceedBudget(id string) {
	t.mu.Lock()
	budget := t.budget
	if e := t.jobs[id]; e != nil && e.spec.RuntimeBudget > 0 {
		budget = e.spec.RuntimeBudget
	}
	t.mu.Unlock()
	t.stop(id, fmt.Errorf("%w after %s", ErrBudget, budget))
}

func (t *Table) removeFromQueueLocked(id string) {
	for i, queued := range t.queue {
		if queued == id {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			return
		}
	}
}

// orderedIDsLocked lists running jobs first so a status query names the job
// that is actually working, not the one waiting behind it.
func (t *Table) orderedIDsLocked() []string {
	seen := make(map[string]struct{}, len(t.jobs))
	var out []string
	add := func(id string) {
		if _, ok := t.jobs[id]; !ok {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		out = append(out, id)
		seen[id] = struct{}{}
	}
	for _, id := range t.runOrder {
		add(id)
	}
	for _, id := range t.queue {
		add(id)
	}
	for id := range t.jobs {
		add(id)
	}
	return out
}

// SetWorkerLimits changes future admission/scheduling bounds. Running work
// retains its slot until completion; lowering concurrency never cancels it.
// A zero queue limit preserves legacy unbounded queuing.
func (t *Table) SetWorkerLimits(concurrent, queued int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxConcurrent = max(1, concurrent)
	t.maxQueued = max(0, queued)
	t.pumpLocked()
}
