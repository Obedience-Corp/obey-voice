package delegation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Delivery contains only a bounded spoken brief. SourceRef is the durable
// Obey delegation ID, so conversation compaction cannot change its identity.
type Delivery struct{ Ticket, SourceRef, Brief string }

// TerminalOutcome is the immutable runtime observation of a terminal worker.
// It is deliberately independent of a provider or turn. Task is the original
// submitted task, retained so provider replacement cannot change its identity.
type TerminalOutcome struct {
	Ticket    string
	SourceRef string
	Task      string
	State     string
	Brief     string
	Err       string
	Started   time.Time
	Ended     time.Time
	Elapsed   time.Duration
	// Termination describes uncertainty when a local stop cannot prove the
	// remote worker ended (for example, "unknown" or "confirmed").
	Termination string
}

// TerminalObserver receives a frozen terminal outcome. Runtime invokes it
// after releasing its state lock and at most once for each job.
type TerminalObserver func(TerminalOutcome)

// ConversationObserver transfers admission and terminal ownership to the
// host conversation authority. Callbacks run outside Runtime's mutex.
type ConversationObserver struct {
	Admitted func(ticket, task string) error
	Started  func(ticket string)
	Finished func(TerminalOutcome) error
}

type job struct {
	deadline     time.Time
	ticket       string
	task         string
	started      time.Time
	snapshot     Snapshot
	cancelReason string
	done         bool
	err          string
	observed     bool
}

// Runtime isolates worker waiting from individual voice turns. The host owns
// Close; it must not pass a turn or speech context as the lifetime context.
// Only one unacknowledged job is admitted, bounding both work and delivery.
type Runtime struct {
	client         Client
	summarize      Summarizer
	timeout        time.Duration
	ctx            context.Context
	stop           context.CancelFunc
	mu             sync.Mutex
	job            *job
	pending        *Delivery
	closed         bool
	ready          chan struct{}
	wg             sync.WaitGroup
	observer       TerminalObserver
	conversation   *ConversationObserver
	admissionCheck func() error
	history        []TerminalOutcome
}

// New verifies the complete backend before the model can see delegation tools.
func New(ctx context.Context, client Client, timeout time.Duration, summarize Summarizer) (*Runtime, error) {
	if client == nil {
		return nil, errors.New("delegation client is required")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Probe(probeCtx); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	lifetime, stop := context.WithCancel(context.Background())
	return &Runtime{client: client, summarize: summarize, timeout: timeout, ctx: lifetime, stop: stop, ready: make(chan struct{}, 1)}, nil
}

// AttachConversation enables host-owned admission and terminal publication.
// Legacy Pending/Ack projection is suppressed while attached.
func (r *Runtime) AttachConversation(observer ConversationObserver) error {
	if observer.Admitted == nil || observer.Finished == nil {
		return errors.New("conversation observer requires admission and finished callbacks")
	}
	r.mu.Lock()
	r.conversation = &observer
	r.mu.Unlock()
	return nil
}

// Start queues dispatch and returns immediately. A ticket acknowledges the
// local queue only; it never claims that Obey has admitted a child yet.
func (r *Runtime) Start(task string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" || len(task) > MaxTaskBytes {
		return "", fmt.Errorf("task must contain between 1 and %d bytes", MaxTaskBytes)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", errors.New("delegation session is closed")
	}
	if r.admissionCheck != nil {
		if err := r.admissionCheck(); err != nil {
			r.mu.Unlock()
			return "", err
		}
	}
	if r.job != nil {
		r.mu.Unlock()
		return "", errors.New("a background task is already running or awaiting delivery")
	}
	ticket := fmt.Sprintf("voice-%x", randomID())
	conversation := r.conversation
	if conversation != nil {
		capability, ok := r.client.(CapabilityProvider)
		if !ok {
			r.mu.Unlock()
			return "", errors.New("managed delegation disabled: client has no verified execution capability")
		}
		if verified := capability.ExecutionCapability(); !verified.Local {
			reason := verified.Reason
			if reason == "" {
				reason = "client did not verify local execution"
			}
			r.mu.Unlock()
			return "", fmt.Errorf("managed delegation disabled: %s", reason)
		}
	}
	j := &job{ticket: ticket, task: task, deadline: time.Now().Add(r.timeout)}
	r.job = j
	r.mu.Unlock()
	admitted := conversation == nil
	if conversation != nil {
		if err := conversation.Admitted(ticket, task); err != nil {
			r.mu.Lock()
			if r.job == j {
				r.job = nil
			}
			r.mu.Unlock()
			return "", err
		}
		admitted = true
	}
	r.mu.Lock()
	if r.job != j || r.closed {
		if r.job == j {
			r.job = nil
		}
		r.mu.Unlock()
		if admitted {
			r.finishLocal(j, "interrupted", "Session closed during admission; remote execution is unknown.", "session closed during admission", "unknown")
		}
		return "", errors.New("delegation session closed during admission")
	}
	r.wg.Add(1)
	go r.run(j, Request{Task: task + workerInstruction, Key: ticket})
	r.mu.Unlock()
	if conversation != nil && conversation.Started != nil {
		conversation.Started(ticket)
	}
	return ticket, nil
}

// SetTerminalObserver attaches the conversation's work authority callback.
// The callback is never invoked while Runtime's mutex is held. Replacing the
// callback is safe before a later job is started; a job's terminal outcome is
// emitted only once.
func (r *Runtime) SetTerminalObserver(observer TerminalObserver) {
	r.mu.Lock()
	r.observer = observer
	r.mu.Unlock()
}

// Task returns the original task for the currently owned job, if any.
func (r *Runtime) Task() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil {
		return ""
	}
	return r.job.task
}

// SetAdmissionCheck installs the host's fast, non-blocking permission check.
// It runs under the runtime lock and must not call back into this runtime.
func (r *Runtime) SetAdmissionCheck(check func() error) {
	r.mu.Lock()
	r.admissionCheck = check
	r.mu.Unlock()
}

// Tasks returns cached live and terminal observations without backend I/O.
// Terminal history is bounded; durable conversation history is host-owned.
func (r *Runtime) Tasks() []TerminalOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]TerminalOutcome(nil), r.history...)
	if j := r.job; j != nil && !j.done {
		state := "running"
		if j.snapshot.ID == "" {
			state = "queued"
		}
		if j.cancelReason != "" {
			state = "stopping"
		}
		out = append(out, TerminalOutcome{Ticket: j.ticket, SourceRef: j.snapshot.ID,
			Task: j.task, State: state, Started: j.started, Err: j.err})
	}
	return out
}

func randomID() [16]byte {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return id
}

// Status is a cached observation, never a synchronous backend wait.
func (r *Runtime) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil {
		return "No background task is pending."
	}
	j := r.job
	if j.err != "" {
		return "Background task status needs attention: " + j.err
	}
	if j.snapshot.ID == "" {
		return "Background task " + j.ticket + " is queued for Obey."
	}
	return "Background task " + j.ticket + " (Obey " + j.snapshot.ID + "): " + j.snapshot.State
}

// Cancel records explicit user intent without blocking a spoken turn. The
// worker loop calls the backend even if cancellation races with admission.
func (r *Runtime) Cancel(ticket string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil || (ticket != r.job.ticket && ticket != r.job.snapshot.ID) {
		return errors.New("unknown background task")
	}
	if !r.job.done {
		r.job.cancelReason = "explicit voice user cancellation"
	}
	return nil
}

// Pending does not consume a brief. Ack is called only after the conductor has
// inserted the projected note, avoiding loss during compaction or retries.
func (r *Runtime) Pending() (Delivery, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		return Delivery{}, false
	}
	return *r.pending, true
}
func (r *Runtime) Ack(ticket string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil && r.pending.Ticket == ticket {
		r.pending = nil
		if r.job.snapshot.Terminal() {
			r.job = nil
		}
	}
}

func (r *Runtime) Close() error {
	r.Stop()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job != nil && r.job.err != "" && r.job.snapshot.State != "interrupted" {
		return fmt.Errorf("%s (ticket %s)", r.job.err, r.job.ticket)
	}
	return nil
}

func (r *Runtime) run(j *job, req Request) {
	defer r.wg.Done()
	r.mu.Lock()
	j.started = time.Now()
	r.mu.Unlock()
	// Admission is allowed to finish independently of barge-in and shutdown so
	// its returned durable ID can always be used for an explicit cancel.
	var snapshot Snapshot
	var err error
	// The daemon may continue provisioning after an RPC disconnect. Reuse the
	// same idempotency key on every retry so uncertainty cannot spawn duplicates.
	for attempt := 0; attempt < 3; attempt++ {
		startCtx, cancel := context.WithTimeout(r.ctx, 45*time.Second)
		snapshot, err = r.client.Start(startCtx, req)
		cancel()
		if err == nil {
			break
		}
		if r.ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		if r.ctx.Err() != nil {
			r.finishLocal(j, "interrupted", "Session stopped before admission was confirmed; remote execution is unknown.", "admission status unknown", "unknown")
		} else {
			r.finishError(j, "Obey admission could not be confirmed. Check Obey before retrying this task.")
		}
		return
	}
	r.update(j, snapshot)
	deadline := j.deadline
	cancellationSent := false
	for !snapshot.Terminal() {
		r.mu.Lock()
		reason := j.cancelReason
		r.mu.Unlock()
		if reason == "" && !cancellationSent && time.Now().After(deadline) {
			reason = "voice task timeout"
		}
		if !cancellationSent && (reason != "" || r.ctx.Err() != nil) {
			if reason == "" {
				reason = "voice session stopped"
			}
			cancelCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			snapshot, err = r.client.Cancel(cancelCtx, snapshot.ID, reason)
			stop()
			if err != nil {
				r.finishError(j, "Obey cancellation could not be confirmed. Check Obey for the task's current state.")
				return
			}
			r.update(j, snapshot)
			// Acceptance of cancel is not proof of terminal cancellation. A normal
			// session keeps awaiting proof; shutdown returns the truthful observation.
			if r.ctx.Err() != nil && !snapshot.Terminal() {
				r.finishLocal(j, "interrupted", "Cancellation was requested, but remote termination was not confirmed.", "remote worker may still be running", "unknown")
				return
			}
			r.mu.Lock()
			j.cancelReason = ""
			r.mu.Unlock()
			cancellationSent = true
		}
		if snapshot.Terminal() {
			break
		}
		if cancellationSent && r.ctx.Err() != nil {
			r.finishLocal(j, "interrupted", "Cancellation was requested, but remote termination was not confirmed.", "remote worker may still be running", "unknown")
			return
		}
		// A short RPC deadline makes explicit cancel responsive while Await's
		// documented disconnect semantics preserve the child.
		waitCtx, stop := context.WithTimeout(r.ctx, 2*time.Second)
		next, waitErr := r.client.Await(waitCtx, snapshot.ID)
		interrupted := waitCtx.Err() != nil
		stop()
		if waitErr != nil {
			if interrupted {
				continue
			}
			r.finishError(j, "Obey could not return the task result. The worker may still be running; check Obey.")
			return
		}
		if next.ID != snapshot.ID {
			r.finishError(j, "Obey returned a result for a different task.")
			return
		}
		snapshot = next
		r.update(j, snapshot)
		if !snapshot.Terminal() {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.ctx.Done():
			}
			timer.Stop()
		}
	}
	if r.ctx.Err() != nil {
		brief := snapshot.Text
		if brief == "" {
			brief = "The task ended with state: " + snapshot.State + "."
		}
		r.finishLocal(j, snapshot.State, brief, "", "confirmed")
		return
	}
	briefCtx, stop := context.WithTimeout(r.ctx, 30*time.Second)
	brief, err := makeBrief(briefCtx, snapshot, r.summarize)
	stop()
	if err != nil {
		r.finishError(j, "The task result could not be prepared for speech.")
		return
	}
	r.mu.Lock()
	j.done = true
	if r.conversation == nil {
		r.pending = &Delivery{Ticket: j.ticket, SourceRef: snapshot.ID, Brief: brief}
	}
	outcome, observer := r.terminalLocked(j, brief, "")
	outcome.Termination = "confirmed"
	conversation := r.conversation
	r.signalReady()
	r.mu.Unlock()
	// Do not call external work authority code while holding Runtime state.
	if observer != nil {
		observer(outcome)
	}
	if conversation != nil {
		_ = conversation.Finished(outcome)
		r.mu.Lock()
		if r.job == j {
			r.job = nil
		}
		r.mu.Unlock()
	}
}
func (r *Runtime) update(j *job, s Snapshot) { r.mu.Lock(); j.snapshot = s; r.mu.Unlock() }
func (r *Runtime) finishError(j *job, message string) {
	r.mu.Lock()
	snapshot := j.snapshot
	r.mu.Unlock()
	if snapshot.ID != "" && !snapshot.Terminal() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stopped, err := r.client.Cancel(ctx, snapshot.ID, "voice could not retrieve task outcome")
		cancel()
		if err == nil && stopped.ID == snapshot.ID {
			r.update(j, stopped)
		}
	}
	r.mu.Lock()
	snapshot = j.snapshot
	r.mu.Unlock()
	state, termination := snapshot.State, "unknown"
	if state == "" {
		state = "failed"
	}
	if snapshot.Terminal() {
		termination = "confirmed"
	} else {
		state = "cleanup_failed"
	}
	r.finishLocal(j, state, message, message, termination)
}

func (r *Runtime) finishLocal(j *job, state, brief, errText, termination string) {
	r.mu.Lock()
	j.done = true
	j.err = errText
	j.snapshot.State = state
	if r.conversation == nil {
		r.pending = &Delivery{Ticket: j.ticket, SourceRef: j.snapshot.ID, Brief: brief}
	}
	outcome, observer := r.terminalLocked(j, brief, errText)
	outcome.Termination = termination
	r.history = append(r.history, outcome)
	if len(r.history) > 50 {
		r.history = append([]TerminalOutcome(nil), r.history[len(r.history)-50:]...)
	}
	conversation := r.conversation
	r.signalReady()
	r.mu.Unlock()
	// The observer belongs to the host/work authority and may perform I/O.
	if observer != nil {
		observer(outcome)
	}
	if conversation != nil {
		_ = conversation.Finished(outcome)
		r.mu.Lock()
		if r.job == j {
			r.job = nil
		}
		r.mu.Unlock()
	}
}

// terminalLocked freezes the terminal observation and consumes the observer
// slot. The returned callback must run after the mutex is released.
func (r *Runtime) terminalLocked(j *job, brief, errText string) (TerminalOutcome, TerminalObserver) {
	ended := time.Now()
	started := j.started
	var elapsed time.Duration
	if !started.IsZero() {
		elapsed = ended.Sub(started)
	}
	outcome := TerminalOutcome{Ticket: j.ticket, SourceRef: j.snapshot.ID, Task: j.task, State: j.snapshot.State, Brief: brief, Err: errText, Started: started, Ended: ended, Elapsed: elapsed}
	if r.observer == nil || j.observed {
		return outcome, nil
	}
	observer := r.observer
	j.observed = true
	return outcome, observer
}

// Ready wakes a host-owned serialized turn driver. It never starts a turn.
func (r *Runtime) Ready() <-chan struct{} { return r.ready }
func (r *Runtime) signalReady() {
	select {
	case r.ready <- struct{}{}:
	default:
	}
}

// NewSession detaches the prior conversation without waiting for admission.
// Both runtimes use the same trusted backend and notification channel; the
// host drops the old runtime's pending view and closes it asynchronously.
func (r *Runtime) NewSession() *Runtime {
	r.Stop()
	r.mu.Lock()
	check := r.admissionCheck
	r.mu.Unlock()
	lifetime, stop := context.WithCancel(context.Background())
	return &Runtime{client: r.client, summarize: r.summarize, timeout: r.timeout, ctx: lifetime, stop: stop, ready: r.ready, admissionCheck: check}
}
func (r *Runtime) Stop() {
	r.mu.Lock()
	r.closed = true
	if r.job != nil && !r.job.done {
		r.job.cancelReason = "voice session stopped"
	}
	r.mu.Unlock()
	r.stop()
}
func (r *Runtime) Enabled() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.closed }
