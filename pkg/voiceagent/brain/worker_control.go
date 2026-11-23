package brain

import (
	"context"
	"errors"
	"sync"
)

// CorrectionState is where a correction to a running job stands.
type CorrectionState string

const (
	// CorrectionPending: accepted, not yet in a request the worker sent.
	CorrectionPending CorrectionState = "pending"
	// CorrectionIncluded: it was in a request the worker sent. It does not
	// mean the worker obeyed it.
	CorrectionIncluded CorrectionState = "included"
	// CorrectionNotApplied: the job could not take it; Reason says why.
	CorrectionNotApplied CorrectionState = "not_applied"
	// CorrectionSuperseded: a cancel arrived first.
	CorrectionSuperseded CorrectionState = "superseded"
	// CorrectionRefused: the job had already finished.
	CorrectionRefused CorrectionState = "refused"
)

// Reasons a correction could not apply.
const (
	NotAppliedLimit   = "limit reached"
	NotAppliedContext = "context limit reached"
	NotAppliedStopped = "the job was stopped"
	NotAppliedFailed  = "the job failed"
)

// toolNotRunCorrection answers a tool call the worker had not started when a
// correction arrived.
const toolNotRunCorrection = "not run: a correction arrived"

// Correction is one correction to a running job and its disposition.
type Correction struct {
	Revision int
	Text     string
	State    CorrectionState
	Reason   string
}

type correction struct {
	Correction
	// taken: appended to the worker's history, its request not yet sent.
	taken bool
}

// WorkerControl carries corrections to one running worker job. Any goroutine
// may add a correction or end the job; only the worker's own goroutine takes
// corrections into its history. Dispositions are reported in order, outside
// the lock.
type WorkerControl struct {
	mu          sync.Mutex
	corrections []correction
	revision    int
	sealed      bool
	// limit is why the worker's last answer could not be continued: a time
	// or request limit had forced, or would force, the final round.
	limit  ForcedAnswer
	outbox []Correction

	notifyMu sync.Mutex
	notify   func(Correction)
}

// NewWorkerControl returns an open control. briefed is how many corrections
// the job's brief already carries, so revisions continue after them. notify
// receives every acceptance and disposition change, in order.
func NewWorkerControl(notify func(Correction), briefed int) *WorkerControl {
	return &WorkerControl{notify: notify, revision: briefed}
}

// Steer adds a correction. It is pending, or refused when the job is over.
func (c *WorkerControl) Steer(text string) Correction {
	c.mu.Lock()
	if c.sealed {
		c.mu.Unlock()
		return Correction{Text: text, State: CorrectionRefused, Reason: "already finished"}
	}
	c.revision++
	item := Correction{Revision: c.revision, Text: text, State: CorrectionPending}
	c.corrections = append(c.corrections, correction{Correction: item})
	c.outbox = append(c.outbox, item)
	c.mu.Unlock()
	c.flush()
	return item
}

// Supersede ends the job for a cancel: every correction not yet sent is
// superseded and later ones are refused.
func (c *WorkerControl) Supersede() { c.end(CorrectionSuperseded, "") }

// Close ends the job without a delivered answer: every correction not yet
// sent could not apply, for reason, and later ones are refused.
func (c *WorkerControl) Close(reason string) { c.end(CorrectionNotApplied, reason) }

func (c *WorkerControl) end(state CorrectionState, reason string) {
	c.mu.Lock()
	c.sealed = true
	c.settleLocked(func(*correction) bool { return true }, state, reason)
	c.mu.Unlock()
	c.flush()
}

// Seal commits the job's answer. It fails while a correction is pending and
// the worker can still take it; the runner then continues the job. When a
// limit stops that, the pending corrections could not apply and limit says
// which, so the answer is delivered cut short.
func (c *WorkerControl) Seal() (sealed bool, limit ForcedAnswer) {
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
	if c.limit == "" {
		return false, ""
	}
	c.sealed = true
	c.settleLocked(func(*correction) bool { return true }, CorrectionNotApplied, NotAppliedLimit)
	return true, c.limit
}

// Included counts corrections that were in a request the worker sent.
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
// when its run ended with err.
func NotAppliedReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrWorkerBudgetExceeded), errors.Is(err, ErrWorkerOutOfTime):
		return NotAppliedLimit
	case errors.Is(err, ErrPromptBudget):
		return NotAppliedContext
	case ctx.Err() != nil:
		return NotAppliedStopped
	default:
		return NotAppliedFailed
	}
}

// waiting reports a correction the worker has not yet taken.
func (c *WorkerControl) waiting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitingLocked()
}

func (c *WorkerControl) waitingLocked() bool {
	for _, item := range c.corrections {
		if item.State == CorrectionPending && !item.taken {
			return true
		}
	}
	return false
}

// take marks the corrections not yet taken as taken and returns their texts,
// in order, for the worker to append to its history.
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

// dispatched records that a request carrying every taken correction was sent.
func (c *WorkerControl) dispatched() {
	c.mu.Lock()
	c.settleLocked(func(item *correction) bool { return item.taken }, CorrectionIncluded, "")
	c.mu.Unlock()
	c.flush()
}

// restore returns taken corrections whose request was never sent to pending,
// after the worker rolled its history back.
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
// because a limit leaves no request for it. It reports whether any was.
func (c *WorkerControl) limitReached() bool {
	c.mu.Lock()
	n := c.settleLocked(func(item *correction) bool { return !item.taken }, CorrectionNotApplied, NotAppliedLimit)
	c.mu.Unlock()
	c.flush()
	return n > 0
}

func (c *WorkerControl) setLimit(limit ForcedAnswer) {
	c.mu.Lock()
	c.limit = limit
	c.mu.Unlock()
}

func (c *WorkerControl) settleLocked(match func(*correction) bool, state CorrectionState, reason string) int {
	n := 0
	for i := range c.corrections {
		item := &c.corrections[i]
		if item.State != CorrectionPending || !match(item) {
			continue
		}
		item.State, item.Reason, item.taken = state, reason, false
		c.outbox = append(c.outbox, item.Correction)
		n++
	}
	return n
}

// flush reports queued changes in the order they were made. One goroutine
// drains at a time, so an acceptance never follows its own disposition.
func (c *WorkerControl) flush() {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	for {
		c.mu.Lock()
		out := c.outbox
		c.outbox = nil
		c.mu.Unlock()
		if len(out) == 0 {
			return
		}
		for _, item := range out {
			if c.notify != nil {
				c.notify(item)
			}
		}
	}
}

// TakesCorrections reports whether a worker provider reads a WorkerControl
// while it runs: the in-process Ollama worker.
func TakesCorrections(p Provider) bool {
	_, ok := p.(*OllamaBrain)
	return ok
}

type requestControlKey struct{}

// requestControl is what chat consults for one worker request: dispatched
// runs when the request is sent, obsolete reports a correction that arrived
// after it was built and before a resend.
type requestControl struct {
	dispatched func()
	obsolete   func() bool
}

// errRequestObsolete abandons a worker request that a correction made stale
// before it was sent again.
var errRequestObsolete = errors.New("a correction arrived before the request was sent")

func withRequestControl(ctx context.Context, rc requestControl) context.Context {
	return context.WithValue(ctx, requestControlKey{}, rc)
}

func requestControlFrom(ctx context.Context) (requestControl, bool) {
	rc, ok := ctx.Value(requestControlKey{}).(requestControl)
	return rc, ok
}

func requestDispatched(ctx context.Context) {
	if rc, ok := requestControlFrom(ctx); ok && rc.dispatched != nil {
		rc.dispatched()
	}
}

func requestObsolete(ctx context.Context) bool {
	rc, ok := requestControlFrom(ctx)
	return ok && rc.obsolete != nil && rc.obsolete()
}
