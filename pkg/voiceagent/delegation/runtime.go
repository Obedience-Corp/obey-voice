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

type job struct {
	deadline     time.Time
	ticket       string
	snapshot     Snapshot
	cancelReason string
	done         bool
	err          string
}

// Runtime isolates worker waiting from individual voice turns. The host owns
// Close; it must not pass a turn or speech context as the lifetime context.
// Only one unacknowledged job is admitted, bounding both work and delivery.
type Runtime struct {
	client    Client
	summarize Summarizer
	timeout   time.Duration
	ctx       context.Context
	stop      context.CancelFunc
	mu        sync.Mutex
	job       *job
	pending   *Delivery
	closed    bool
	ready     chan struct{}
	wg        sync.WaitGroup
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

// Start queues dispatch and returns immediately. A ticket acknowledges the
// local queue only; it never claims that Obey has admitted a child yet.
func (r *Runtime) Start(task string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" || len(task) > MaxTaskBytes {
		return "", errors.New(fmt.Sprintf("task must contain between 1 and %d bytes", MaxTaskBytes))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", errors.New("delegation session is closed")
	}
	if r.job != nil {
		return "", errors.New("a background task is already running or awaiting delivery")
	}
	ticket := fmt.Sprintf("voice-%x", randomID())
	j := &job{ticket: ticket, deadline: time.Now().Add(r.timeout)}
	r.job = j
	r.wg.Add(1)
	go r.run(j, Request{Task: task + workerInstruction, Key: ticket})
	return ticket, nil
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
	r.mu.Lock()
	r.closed = true
	if r.job != nil && !r.job.done {
		r.job.cancelReason = "voice session stopped"
	}
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job != nil && r.job.err != "" {
		return errors.New(r.job.err)
	}
	return nil
}

func (r *Runtime) run(j *job, req Request) {
	defer r.wg.Done()
	// Admission is allowed to finish independently of barge-in and shutdown so
	// its returned durable ID can always be used for an explicit cancel.
	var snapshot Snapshot
	var err error
	// The daemon may continue provisioning after an RPC disconnect. Reuse the
	// same idempotency key on every retry so uncertainty cannot spawn duplicates.
	for attempt := 0; attempt < 3; attempt++ {
		startCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		snapshot, err = r.client.Start(startCtx, req)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		r.finishError(j, "Obey admission could not be confirmed. Check Obey before retrying this task.")
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
	briefCtx, stop := context.WithTimeout(r.ctx, 30*time.Second)
	brief, err := makeBrief(briefCtx, snapshot, r.summarize)
	stop()
	if err != nil {
		r.finishError(j, "The task result could not be prepared for speech.")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j.done = true
	r.pending = &Delivery{Ticket: j.ticket, SourceRef: snapshot.ID, Brief: brief}
	r.signalReady()
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
	defer r.mu.Unlock()
	j.done = true
	j.err = message
	r.pending = &Delivery{Ticket: j.ticket, SourceRef: j.snapshot.ID, Brief: message}
	r.signalReady()
}

// Ready wakes a host-owned serialized turn driver. It never starts a turn.
func (r *Runtime) Ready() <-chan struct{} { return r.ready }
func (r *Runtime) signalReady() {
	select {
	case r.ready <- struct{}{}:
	default:
	}
}
