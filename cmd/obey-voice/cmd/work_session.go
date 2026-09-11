package cmd

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// workCheckpoint coalesces copied state after publication. Workers never wait
// for disk, and a delayed revision cannot overwrite a newer checkpoint. The
// normal session file owns durable outcomes; transcript events are diagnostics.
type workCheckpoint struct {
	pipeline *pipeline.Pipeline
	store    *session.Store
	session  *session.Session
	scope    work.WorkScope // owned by run

	mu      sync.Mutex
	pending *work.Snapshot
	closed  bool
	err     error
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func bindWorkSession(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session, store *session.Store) (*workCheckpoint, error) {
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
	w := &workCheckpoint{pipeline: p, store: store, session: sess, scope: snapshot.Scope,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	p.SetWorkObserver(w.submit)
	go w.run()
	return w, nil
}

func (w *workCheckpoint) submit(snapshot work.Snapshot) {
	if w == nil || snapshot.Scope.SessionID != w.session.ID {
		return
	}
	w.mu.Lock()
	// Check authority while serializing submissions. Otherwise a callback
	// paused before taking this lock could replace a newer scope's pending
	// checkpoint after clear has already published it.
	if w.closed || snapshot.Scope != w.pipeline.WorkSnapshot().Scope || (w.pending != nil && w.pending.Scope == snapshot.Scope && w.pending.Revision >= snapshot.Revision) {
		w.mu.Unlock()
		return
	}
	w.pending = &snapshot
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *workCheckpoint) retry() {
	if w != nil {
		w.submit(w.pipeline.WorkSnapshot())
	}
}

func (w *workCheckpoint) run() {
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
			snapshot := w.pending
			w.pending = nil
			w.mu.Unlock()
			if snapshot == nil {
				break
			}
			w.checkpoint(*snapshot)
		}
		if stopping {
			return
		}
	}
}

func (w *workCheckpoint) checkpoint(snapshot work.Snapshot) {
	var err error
	if snapshot.Scope == w.scope {
		err = w.store.SaveWork(w.session, snapshot)
	} else {
		err = w.store.BeginWork(w.session, snapshot, &w.scope)
	}
	if errors.Is(err, session.ErrWorkRevisionStale) {
		return // A later copied revision is already durable.
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
	w.mu.Unlock()
	if err != nil && w.pipeline.Events != nil {
		w.pipeline.Events.Emit(events.Error{Stage: "work_checkpoint", Message: fmt.Sprintf("Background result checkpoint failed: %v", err)})
	}
}

// Close drains the last copied state after the host retires its work scope.
// Failed writes remain visible to cleanup; they are not reported persisted.
func (w *workCheckpoint) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		w.retry()
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

func closeWorkSession(p *pipeline.Pipeline, checkpoint *workCheckpoint) error {
	return errors.Join(p.CloseWork(), checkpoint.Close())
}
