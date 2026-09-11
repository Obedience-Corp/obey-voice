package worksession

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Checkpoint coalesces copied state after publication. Workers never wait
// for disk, and a delayed revision cannot overwrite a newer checkpoint. The
// normal session file owns durable outcomes; transcript events are diagnostics.
type Checkpoint struct {
	pipeline *pipeline.Pipeline
	store    *session.Store
	session  *session.Session
	scope    work.WorkScope // owned by run

	mu                      sync.Mutex
	pending                 *checkpointRequest
	closed                  bool
	err                     error
	waitGeneration          uint64
	completedWaitGeneration uint64
	completedWaitErr        error
	wake                    chan struct{}
	stop                    chan struct{}
	done                    chan struct{}
	once                    sync.Once
}

// checkpointRequest carries the Wait generation through the coalescing queue.
// A persistence operation already in flight when Wait starts must not satisfy
// or fail that new wait; only the fresh retry may do so.
type checkpointRequest struct {
	snapshot       work.Snapshot
	waitGeneration uint64
}

// Bind starts one coalescing persistence adapter for a conversation work
// scope. A disabled config deliberately preserves the command's no-op path.
func Bind(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session, store *session.Store) (*Checkpoint, error) {
	if cfg == nil || !cfg.VoiceWorkEnabled {
		return nil, nil
	}
	var prior *work.WorkScope
	if sess.Work != nil {
		scope := sess.Work.Scope
		prior = &scope
	}
	if err := p.EnableConversationWork(sess.ID, sess.Work, cfg); err != nil {
		return nil, fmt.Errorf("prepare conversation work: %w", err)
	}
	snapshot := p.WorkSnapshot()
	if err := store.BeginWork(sess, snapshot, prior); err != nil {
		return nil, errors.Join(fmt.Errorf("start conversation checkpoint: %w", err), p.CloseWork())
	}
	if c := p.WorkCoordinator(); c != nil {
		if err := c.AcknowledgePersisted(snapshot.Scope, snapshot.Revision); err != nil {
			return nil, fmt.Errorf("acknowledge initial work checkpoint: %w", err)
		}
	}
	w := &Checkpoint{pipeline: p, store: store, session: sess, scope: snapshot.Scope,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	p.SetWorkObserver(w.Submit)
	go w.run()
	return w, nil
}

// Submit queues a copied work snapshot, coalescing superseded revisions.
func (w *Checkpoint) Submit(snapshot work.Snapshot) {
	w.enqueue(snapshot, 0)
}

func (w *Checkpoint) enqueue(snapshot work.Snapshot, waitGeneration uint64) {
	if w == nil || snapshot.Scope.SessionID != w.session.ID {
		return
	}
	w.mu.Lock()
	// Check authority while serializing submissions. Otherwise a callback
	// paused before taking this lock could replace a newer scope's pending
	// checkpoint after clear has already published it.
	if w.closed || snapshot.Scope != w.pipeline.WorkSnapshot().Scope {
		w.mu.Unlock()
		return
	}
	if w.pending != nil && w.pending.snapshot.Scope == snapshot.Scope && w.pending.snapshot.Revision >= snapshot.Revision {
		if waitGeneration > w.pending.waitGeneration {
			w.pending.waitGeneration = waitGeneration
		}
		w.mu.Unlock()
		return
	}
	// A newer snapshot supersedes the pending one, but a waiter attached to
	// that older snapshot still needs the result of this coalesced write.
	if w.pending != nil && w.pending.waitGeneration > waitGeneration {
		waitGeneration = w.pending.waitGeneration
	}
	w.pending = &checkpointRequest{snapshot: snapshot, waitGeneration: waitGeneration}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Retry queues the current copied work state for a final close drain.
func (w *Checkpoint) Retry() {
	if w != nil {
		w.Submit(w.pipeline.WorkSnapshot())
	}
}

// Wait queues the current snapshot and waits until the coordinator reports
// that its revision is durably acknowledged. It leaves the adapter running so
// the host can claim and deliver the result on the same work scope.
func (w *Checkpoint) Wait(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	w.waitGeneration++
	waitGeneration := w.waitGeneration
	w.mu.Unlock()
	w.enqueue(w.pipeline.WorkSnapshot(), waitGeneration)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := w.pipeline.WorkSnapshot()
		if snapshot.Revision <= snapshot.PersistedRevision {
			return nil
		}
		w.mu.Lock()
		completed := w.completedWaitGeneration >= waitGeneration
		err := w.completedWaitErr
		w.mu.Unlock()
		if completed && err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *Checkpoint) run() {
	defer close(w.done)
	stopping := false
	for {
		if !stopping {
			select {
			case <-w.wake:
			case <-w.stop:
				stopping = true
			}
		}
		for {
			w.mu.Lock()
			request := w.pending
			w.pending = nil
			w.mu.Unlock()
			if request == nil {
				break
			}
			w.checkpoint(*request)
		}
		if stopping {
			return
		}
	}
}

func (w *Checkpoint) checkpoint(request checkpointRequest) {
	snapshot := request.snapshot
	var err error
	if snapshot.Scope == w.scope {
		err = w.store.SaveWork(w.session, snapshot)
	} else {
		err = w.store.BeginWork(w.session, snapshot, &w.scope)
	}
	if errors.Is(err, session.ErrWorkRevisionStale) {
		err = nil // A later copied revision is already durable.
	}
	if err == nil {
		w.scope = snapshot.Scope
		if c := w.pipeline.WorkCoordinator(); c != nil {
			err = c.AcknowledgePersisted(snapshot.Scope, snapshot.Revision)
			if errors.Is(err, work.ErrStaleScope) {
				// This exact old revision was saved, but the conversation has
				// already advanced. Its new scope has its own queued checkpoint.
				err = nil
			}
		}
	}
	w.mu.Lock()
	w.err = err
	if request.waitGeneration > w.completedWaitGeneration {
		w.completedWaitGeneration = request.waitGeneration
		w.completedWaitErr = err
	}
	w.mu.Unlock()
	if err != nil && w.pipeline.Events != nil {
		w.pipeline.Events.Emit(events.Error{Stage: "work_checkpoint", Message: fmt.Sprintf("Background result checkpoint failed: %v", err)})
	}
}

// Close drains the last copied state after the host retires its work scope.
// Failed writes remain visible to cleanup; they are not reported persisted.
func (w *Checkpoint) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		w.Retry()
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		close(w.stop)
	})
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
