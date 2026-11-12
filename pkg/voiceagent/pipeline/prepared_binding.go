package pipeline

import (
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// PreparedBrainReplacement owns a candidate responder until the host's
// serialized lifecycle boundary commits it. Preparation may attach the
// candidate's managed runtime, but it does not alter the current responder,
// coordinator binding, managed registry, or job state.
//
// Commit calls persist before publishing any in-memory binding. The callback
// must not perform another lifecycle transition; the host owns that
// serialization. If persistence fails, the candidate runtime is discarded and
// the current responder remains untouched.
type PreparedBrainReplacement struct {
	pipeline  *Pipeline
	agency    *Agency
	next      brain.Provider
	previous  brain.Provider
	worker    brain.Provider
	cfg       *config.Config
	workerErr error

	coordinator *work.Coordinator
	scope       work.WorkScope
	binding     brain.DelegationBinding
	runtime     *delegation.Runtime
	noOp        bool
	closed      bool
	committed   bool
	// retainPrevious leaves the replaced responder to the caller.
	retainPrevious bool
}

// RetainPrevious hands the replaced responder to the caller instead of
// retiring it on commit. The caller then owns closing it.
func (r *PreparedBrainReplacement) RetainPrevious() {
	if r != nil {
		r.retainPrevious = true
	}
}

// PrepareConversationBrain validates and prepares a responder replacement.
// Fallible candidate binding work happens before persistence; the old
// responder and its work authority are not touched until Commit succeeds.
func (p *Pipeline) PrepareConversationBrain(next brain.Provider, worker brain.Provider, cfg *config.Config, workerErr error) (*PreparedBrainReplacement, error) {
	if p == nil {
		return nil, fmt.Errorf("pipeline: nil pipeline")
	}
	if next == nil {
		return nil, fmt.Errorf("pipeline: replacement brain is required")
	}
	prepared := &PreparedBrainReplacement{
		pipeline:  p,
		agency:    p.Agency,
		next:      next,
		previous:  p.Brain,
		worker:    worker,
		cfg:       cloneConfig(cfg),
		workerErr: workerErr,
	}
	if p.Brain == next {
		prepared.noOp = true
	}

	if p.Agency == nil {
		return prepared, nil
	}
	c, scope := p.Agency.workAuthority()
	if c != nil {
		if snapshot := c.Snapshot(scope); snapshot.Scope != scope {
			return nil, work.ErrStaleScope
		} else if snapshot.Retired {
			return nil, work.ErrRetired
		}
		prepared.coordinator, prepared.scope = c, scope
		if !prepared.noOp {
			if err := p.Agency.prepareManaged(next, c, scope, cfg, prepared); err != nil {
				return nil, errors.Join(err, prepared.discard())
			}
		}
	}
	return prepared, nil
}

// Commit persists the host session, then publishes the prepared responder.
// The coordinator lock is never held while persist runs. A nil callback is
// accepted for callers that only need the compatibility replacement path.
func (r *PreparedBrainReplacement) Commit(persist func() error) error {
	if r == nil {
		return errors.New("pipeline: nil prepared brain replacement")
	}
	if r.closed {
		return errors.New("pipeline: prepared brain replacement is discarded")
	}
	if r.committed {
		return errors.New("pipeline: prepared brain replacement is already committed")
	}
	if r.pipeline.Brain != r.previous || r.pipeline.Agency != r.agency {
		return errors.Join(errors.New("pipeline: prepared responder belongs to a previous binding"), r.discard())
	}
	if r.coordinator != nil {
		snapshot := r.coordinator.Snapshot(r.scope)
		if snapshot.Scope != r.scope {
			return errors.Join(work.ErrStaleScope, r.discard())
		}
		if snapshot.Retired {
			return errors.Join(work.ErrRetired, r.discard())
		}
	}
	if persist != nil {
		if err := persist(); err != nil {
			return errors.Join(err, r.discard())
		}
	}

	// Publication is deliberately a short, infallible sequence after the
	// durable callback has succeeded. Host serialization makes the scope check
	// above the commit fence; RevokeBinding can therefore only fail if a caller
	// violates that lifecycle contract.
	r.publish()
	r.committed = true
	return nil
}

// Discard releases any candidate runtime that was prepared but not committed.
func (r *PreparedBrainReplacement) Discard() error {
	if r == nil || r.committed {
		return nil
	}
	return r.discard()
}

func (r *PreparedBrainReplacement) discard() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	if r.runtime == nil {
		return nil
	}
	if r.binding != nil {
		// Detach before stopping so an unused candidate cannot retain a
		// conversation observer after a failed durable write.
		_ = r.binding.DetachDelegation()
	}
	runtime := r.runtime
	r.runtime = nil
	runtime.Stop()
	return runtime.Close()
}

func (r *PreparedBrainReplacement) publish() {
	old := r.pipeline.Brain
	if r.noOp {
		return
	}
	if r.coordinator != nil {
		// The scope was checked immediately before persistence and the host
		// dispatcher serializes lifecycle transitions. Keep the coordinator's
		// error-bearing API at this boundary, but do not make publication
		// rollbackable after persistence has succeeded.
		_ = r.coordinator.RevokeBinding(r.scope)
	}
	r.pipeline.Brain = r.next
	if r.agency != nil {
		r.agency.bindConversationWorkers(r.next)
		r.agency.ReplaceWorker(r.worker, r.cfg, r.workerErr)
	}
	if r.agency != nil && r.runtime != nil {
		r.agency.workMu.Lock()
		r.agency.managed[r.runtime] = struct{}{}
		r.agency.workMu.Unlock()
		r.agency.selectManaged(r.runtime)
	} else if r.agency != nil && r.coordinator != nil {
		r.agency.selectManaged(nil)
	}
	if old != nil && !r.retainPrevious {
		r.pipeline.RetireBrain(old)
	}
}
