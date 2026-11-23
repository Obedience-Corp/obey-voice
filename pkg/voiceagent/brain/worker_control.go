package brain

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CorrectionState is where a correction to a job stands.
type CorrectionState string

const (
	// CorrectionPending: accepted, not yet answered in any request.
	CorrectionPending CorrectionState = "pending"
	// CorrectionIncluded: a request carrying it drew a response. It does not
	// mean the worker obeyed it.
	CorrectionIncluded CorrectionState = "included"
	// CorrectionBriefed: in the brief of a worker that cannot report when a
	// request was answered.
	CorrectionBriefed CorrectionState = "briefed"
	// CorrectionNotApplied: the job could not take it; Reason says why.
	CorrectionNotApplied CorrectionState = "not_applied"
	// CorrectionSuperseded: a cancel arrived first.
	CorrectionSuperseded CorrectionState = "superseded"
	// CorrectionRefused: the job had already finished. Never recorded.
	CorrectionRefused CorrectionState = "refused"
	// CorrectionRejected: the running worker cannot take input; Reason says
	// why. Never recorded.
	CorrectionRejected CorrectionState = "rejected"
)

// Reasons a correction could not apply.
const (
	NotAppliedLimit   = "limit reached"
	NotAppliedContext = "context limit reached"
	NotAppliedStopped = "the job was stopped"
	NotAppliedFailed  = "the job failed"
)

// DispositionSuperseded marks a worker answer a correction replaced.
const DispositionSuperseded = "superseded"

// ToolNotRunCorrection answers a tool call the worker had not started when a
// correction arrived.
const ToolNotRunCorrection = "not run: a correction arrived"

// Correction is one correction to a job and its disposition.
type Correction struct {
	Revision int
	Text     string
	State    CorrectionState
	Reason   string
	// Brief marks a correction given before the job started, which its
	// worker reads in its brief.
	Brief bool
}

type correction struct {
	Correction
	// taken: in the worker's input, its request not yet answered.
	taken bool
}

// WorkerControl carries corrections to one job from admission to its end.
// Any goroutine may add a correction or end the job; only the worker's own
// goroutine takes corrections into its history. Acceptances and dispositions
// are reported in order, with no lock held.
type WorkerControl struct {
	mu          sync.Mutex
	corrections []correction
	revision    int
	started     bool
	noInput     string
	sealed      bool
	finishing   bool // an image repair runs; no more corrections
	outbox      []Correction
	draining    bool
	notify      func(Correction)

	// Worker goroutine only, carried across the runs of one job.
	callSeq  map[string]int
	hookIDs  map[string]struct{}
	skillCtx string
}

// NewWorkerControl returns an open control. notify receives every acceptance
// and disposition change, in order, and must not block for long.
func NewWorkerControl(notify func(Correction)) *WorkerControl {
	return &WorkerControl{notify: notify}
}

// Steer adds a correction: pending (for the brief, before the job starts),
// refused once the job is over, or rejected when the running worker cannot
// take input.
func (c *WorkerControl) Steer(text string) Correction {
	c.mu.Lock()
	switch {
	case c.finishing:
		c.mu.Unlock()
		return Correction{Text: text, State: CorrectionRejected, Reason: "the job is finishing"}
	case c.sealed:
		c.mu.Unlock()
		return Correction{Text: text, State: CorrectionRefused, Reason: "already finished"}
	case c.started && c.noInput != "":
		c.mu.Unlock()
		return Correction{Text: text, State: CorrectionRejected, Reason: c.noInput}
	}
	c.revision++
	item := Correction{Revision: c.revision, Text: text, State: CorrectionPending, Brief: !c.started}
	c.corrections = append(c.corrections, correction{Correction: item})
	c.outbox = append(c.outbox, item)
	c.mu.Unlock()
	c.flush()
	return item
}

// Begin is the worker taking its first input. It returns the corrections
// given before the start, in order, for the brief. A worker that takes input
// reports them included once its first request is answered. For one that
// cannot, they are briefed, and later corrections are rejected with noInput.
func (c *WorkerControl) Begin(takesInput bool, noInput string) []string {
	c.mu.Lock()
	c.started = true
	if c.sealed {
		c.mu.Unlock()
		return nil
	}
	if !takesInput {
		c.noInput = noInput
	}
	var texts []string
	for i := range c.corrections {
		item := &c.corrections[i]
		if item.State != CorrectionPending {
			continue
		}
		texts = append(texts, item.Text)
		if takesInput {
			item.taken = true
			continue
		}
		item.State = CorrectionBriefed
		c.outbox = append(c.outbox, item.Correction)
	}
	c.mu.Unlock()
	c.flush()
	return texts
}

// Supersede ends the job for a cancel: every correction not yet answered is
// superseded and later ones are refused.
func (c *WorkerControl) Supersede() { c.end(CorrectionSuperseded, "") }

// Close ends the job: every correction not yet answered could not apply, for
// reason, and later ones are refused.
func (c *WorkerControl) Close(reason string) { c.end(CorrectionNotApplied, reason) }

func (c *WorkerControl) end(state CorrectionState, reason string) {
	c.Decide(state, reason)
	c.flush()
}

// Decide ends the job as Supersede or Close do, without reporting: the caller
// may hold a lock no subscriber may wait on, and reports later with Flush.
func (c *WorkerControl) Decide(state CorrectionState, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishing = false
	if c.sealed {
		return
	}
	c.sealed = true
	c.settleLocked(func(*correction) bool { return true }, state, reason)
}

// Flush reports the changes decided so far, in order.
func (c *WorkerControl) Flush() { c.flush() }

// CloseUnlessWaiting closes the job, as Close does, unless a correction is
// waiting, which the job may still take instead. It reports whether it closed.
func (c *WorkerControl) CloseUnlessWaiting(reason string) bool {
	c.mu.Lock()
	if c.waitingLocked() {
		c.mu.Unlock()
		return false
	}
	if !c.sealed {
		c.sealed = true
		c.settleLocked(func(*correction) bool { return true }, CorrectionNotApplied, reason)
	}
	c.mu.Unlock()
	c.flush()
	return true
}

// Seal commits the job's answer. It fails while a correction is waiting and
// limit is empty; the runner then continues the job. A limit that leaves no
// request for the waiting corrections settles them as unable to apply and is
// returned, so the answer in hand is delivered cut short.
func (c *WorkerControl) Seal(limit ForcedAnswer) (sealed bool, applied ForcedAnswer) {
	c.mu.Lock()
	defer c.flush()
	defer c.mu.Unlock()
	if c.sealed {
		return true, ""
	}
	if !c.waitingLocked() {
		c.sealed = true
		return true, ""
	}
	if limit == "" {
		return false, ""
	}
	c.sealed = true
	c.settleLocked(func(*correction) bool { return true }, CorrectionNotApplied, NotAppliedLimit)
	return true, limit
}

// Waiting reports a correction the worker has not yet taken.
func (c *WorkerControl) Waiting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitingLocked()
}

// Included counts corrections answered in a request the worker sent.
func (c *WorkerControl) Included() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, item := range c.corrections {
		if item.State == CorrectionIncluded {
			n++
		}
	}
	return n
}

// Corrected reports that the job accepted a correction, whatever became of it.
func (c *WorkerControl) Corrected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.corrections) > 0
}

// BeginRepair admits an image repair for a job that took no correction, and
// closes it to corrections while the repair runs. It fails when the job has
// accepted one, which the job takes instead.
func (c *WorkerControl) BeginRepair() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		// The job already ended; the repair fails on its own.
		return true
	}
	if len(c.corrections) > 0 {
		return false
	}
	c.sealed, c.finishing = true, true
	return true
}

// Corrections is every accepted correction, in order.
func (c *WorkerControl) Corrections() []Correction {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Correction, len(c.corrections))
	for i, item := range c.corrections {
		out[i] = item.Correction
	}
	return out
}

// NotAppliedReason is the reason a job's open corrections could not apply
// when its run ended with err and was not stopped by the job table.
func NotAppliedReason(err error) string {
	switch {
	case errors.Is(err, ErrWorkerBudgetExceeded), errors.Is(err, ErrWorkerOutOfTime), errors.Is(err, context.DeadlineExceeded):
		return NotAppliedLimit
	case errors.Is(err, ErrPromptBudget):
		return NotAppliedContext
	default:
		return NotAppliedFailed
	}
}

// WorkerLimitDue reports a time or request limit that leaves a worker no
// request but its answer: the deadline's reserve has begun, measured from
// start, or one request is left.
func WorkerLimitDue(ctx context.Context, budget *WorkerBudget, start time.Time) ForcedAnswer {
	deadline, timed := WorkerDeadline(ctx)
	if timed && time.Until(deadline) <= workerTimeReserve(deadline.Sub(start)) {
		return ForcedByTime
	}
	if left, counted := budget.RemainingRequests(); counted && left <= 1 {
		return ForcedByRequests
	}
	return ""
}

func (c *WorkerControl) waiting() bool { return c.Waiting() }

func (c *WorkerControl) waitingLocked() bool {
	for _, item := range c.corrections {
		if item.State == CorrectionPending && !item.taken {
			return true
		}
	}
	return false
}

// take marks the waiting corrections taken and returns their texts, in
// order, for the worker to append to its history.
func (c *WorkerControl) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var texts []string
	for i := range c.corrections {
		item := &c.corrections[i]
		if item.State == CorrectionPending && !item.taken {
			item.taken = true
			texts = append(texts, item.Text)
		}
	}
	return texts
}

// answered records that a request carrying every taken correction drew a
// response. A correction a cancel settled stays settled.
func (c *WorkerControl) answered() {
	c.mu.Lock()
	c.settleLocked(func(item *correction) bool { return item.taken }, CorrectionIncluded, "")
	c.mu.Unlock()
	c.flush()
}

// restore returns taken corrections no request answered to waiting, after
// the worker rolled its history back.
func (c *WorkerControl) restore() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.corrections {
		if c.corrections[i].State == CorrectionPending {
			c.corrections[i].taken = false
		}
	}
}

// limitReached settles every correction not yet taken as unable to apply
// because a limit leaves no request for it.
func (c *WorkerControl) limitReached() {
	c.mu.Lock()
	c.settleLocked(func(item *correction) bool { return !item.taken }, CorrectionNotApplied, NotAppliedLimit)
	c.mu.Unlock()
	c.flush()
}

// Release drops what the worker kept across the runs of the job. Call it on
// the worker's goroutine once the job's runs are over.
func (c *WorkerControl) Release() {
	c.callSeq, c.hookIDs, c.skillCtx = nil, nil, ""
}

// toolIDs is the job's tool hook id allocator, shared by every run.
func (c *WorkerControl) toolIDs() (map[string]int, map[string]struct{}) {
	if c.callSeq == nil {
		c.callSeq, c.hookIDs = map[string]int{}, map[string]struct{}{}
	}
	return c.callSeq, c.hookIDs
}

func (c *WorkerControl) settleLocked(match func(*correction) bool, state CorrectionState, reason string) {
	for i := range c.corrections {
		item := &c.corrections[i]
		if item.State != CorrectionPending || !match(item) {
			continue
		}
		item.State, item.Reason, item.taken = state, reason, false
		c.outbox = append(c.outbox, item.Correction)
	}
}

// flush reports queued changes in the order they were made. One goroutine
// drains at a time with no lock held; a change made meanwhile, even by a
// notify callback, is queued and delivered by that drainer.
func (c *WorkerControl) flush() {
	c.mu.Lock()
	if c.draining {
		c.mu.Unlock()
		return
	}
	c.draining = true
	for len(c.outbox) > 0 {
		out := c.outbox
		c.outbox = nil
		c.mu.Unlock()
		for _, item := range out {
			if c.notify != nil {
				c.notify(item)
			}
		}
		c.mu.Lock()
	}
	c.draining = false
	c.mu.Unlock()
}

// TakesCorrections reports whether a worker provider reads a WorkerControl
// while it runs: the in-process Ollama worker.
func TakesCorrections(p Provider) bool {
	_, ok := p.(*OllamaBrain)
	return ok
}

type requestControlKey struct{}

// requestControl is what chat consults for one worker request: answered runs
// when the request draws its first response, obsolete reports a correction
// that arrived after the request was built.
type requestControl struct {
	answered func()
	obsolete func() bool
}

// errRequestObsolete abandons a worker request that a correction made stale
// before it was sent.
var errRequestObsolete = errors.New("a correction arrived before the request was sent")

func withRequestControl(ctx context.Context, rc requestControl) context.Context {
	return context.WithValue(ctx, requestControlKey{}, rc)
}

func requestControlFrom(ctx context.Context) (requestControl, bool) {
	rc, ok := ctx.Value(requestControlKey{}).(requestControl)
	return rc, ok
}

func requestAnswered(ctx context.Context) {
	if rc, ok := requestControlFrom(ctx); ok && rc.answered != nil {
		rc.answered()
	}
}

func requestObsolete(ctx context.Context) bool {
	rc, ok := requestControlFrom(ctx)
	return ok && rc.obsolete != nil && rc.obsolete()
}
