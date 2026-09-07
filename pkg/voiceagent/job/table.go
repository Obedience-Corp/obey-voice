package job

import (
	"context"
	"fmt"
	"sync"
	"time"
)

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
	Task string
	Run  func(ctx context.Context) (result string, err error)
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
}

// Elapsed reports how long the job has been running, or zero if it never started.
func (i Info) Elapsed() time.Duration {
	if i.Started.IsZero() {
		return 0
	}
	return time.Since(i.Started)
}

// DoneCallback fires once per job when it reaches a terminal state.
type DoneCallback func(id string, state State, result string, err error)

// Table holds async jobs that outlive one conversational turn.
type Table struct {
	mu       sync.Mutex
	lifetime context.Context
	closed   bool
	nextID   int
	jobs     map[string]*entry
	queue    []string
	running  string
	onDone   DoneCallback
}

type entry struct {
	info   Info
	spec   Spec
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTable constructs a job table. lifetime scopes jobs past turn cancellation;
// callers typically pass the pipeline or agent context.
func NewTable(lifetime context.Context) *Table {
	if lifetime == nil {
		lifetime = context.Background()
	}
	return &Table{
		lifetime: lifetime,
		jobs:     make(map[string]*entry),
	}
}

// OnDone registers a callback for terminal job states.
func (t *Table) OnDone(cb DoneCallback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onDone = cb
}

// Start enqueues work. A second spawn while one is active queues behind it (Q2).
// parent is ignored for cancellation — jobs use context.WithoutCancel(lifetime).
func (t *Table) Start(parent context.Context, spec Spec) (string, error) {
	_ = parent
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", fmt.Errorf("job table closed")
	}
	t.nextID++
	id := fmt.Sprintf("job-%d", t.nextID)
	jobCtx, cancel := context.WithCancel(WithID(context.WithoutCancel(t.lifetime), id))
	e := &entry{
		info:   Info{ID: id, Task: spec.Task, State: StateQueued},
		spec:   spec,
		ctx:    jobCtx,
		cancel: cancel,
	}
	t.jobs[id] = e
	t.queue = append(t.queue, id)
	t.pumpLocked()
	return id, nil
}

// Abort cancels a queued or running job. Missing ids are a no-op.
func (t *Table) Abort(id string) {
	t.mu.Lock()
	e, ok := t.jobs[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	switch e.info.State {
	case StateDone, StateFailed, StateAborted:
		t.mu.Unlock()
		return
	}
	if e.info.State == StateQueued {
		e.info.State = StateAborted
		t.removeFromQueueLocked(id)
		if e.cancel != nil {
			e.cancel()
		}
		cb := t.finishLocked(id, StateAborted, "", fmt.Errorf("aborted"))
		t.mu.Unlock()
		if cb != nil {
			cb(id, StateAborted, "", fmt.Errorf("aborted"))
		}
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	t.mu.Unlock()
	// running: goroutine observes ctx cancel and marks aborted.
}

// Status returns all known jobs in start order.
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
	return t.running != "" || len(t.queue) > 0
}

// Close rejects new jobs. It does not cancel running work.
func (t *Table) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
}

func (t *Table) pumpLocked() {
	if t.running != "" || len(t.queue) == 0 {
		return
	}
	id := t.queue[0]
	t.queue = t.queue[1:]
	e := t.jobs[id]
	e.info.State = StateRunning
	t.running = id
	go t.run(e.ctx, id)
}

func (t *Table) run(jobCtx context.Context, id string) {
	var result string
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("worker panic: %v", r)
		}
		state := StateDone
		if err != nil {
			if jobCtx.Err() != nil {
				state = StateAborted
			} else {
				state = StateFailed
			}
		}
		t.mu.Lock()
		if e, ok := t.jobs[id]; ok && e.info.State == StateAborted {
			state = StateAborted
		}
		cb := t.finishLocked(id, state, result, err)
		t.mu.Unlock()
		if cb != nil {
			cb(id, state, result, err)
		}
	}()

	t.mu.Lock()
	e := t.jobs[id]
	if e != nil && e.spec.Run != nil {
		e.info.Started = time.Now()
	}
	t.mu.Unlock()
	if e == nil || e.spec.Run == nil {
		err = fmt.Errorf("job %s: no run function", id)
		return
	}
	result, err = e.spec.Run(jobCtx)
}

// finishLocked marks a job terminal and starts the next queued job. Caller must hold t.mu.
// It returns the done callback to invoke after releasing the lock.
func (t *Table) finishLocked(id string, state State, result string, err error) DoneCallback {
	e, ok := t.jobs[id]
	if !ok {
		return nil
	}
	e.info.State = state
	e.info.Result = result
	if err != nil {
		e.info.Err = err.Error()
	}
	if t.running == id {
		t.running = ""
	}
	t.pumpLocked()
	return t.onDone
}

func (t *Table) removeFromQueueLocked(id string) {
	for i, queued := range t.queue {
		if queued == id {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			return
		}
	}
}

func (t *Table) orderedIDsLocked() []string {
	seen := make(map[string]struct{}, len(t.jobs))
	var out []string
	for _, id := range t.queue {
		if _, ok := t.jobs[id]; ok {
			if _, dup := seen[id]; !dup {
				out = append(out, id)
				seen[id] = struct{}{}
			}
		}
	}
	if t.running != "" {
		if _, ok := seen[t.running]; !ok {
			out = append(out, t.running)
			seen[t.running] = struct{}{}
		}
	}
	for id := range t.jobs {
		if _, ok := seen[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
