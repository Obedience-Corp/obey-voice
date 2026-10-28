package pipeline

import (
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"strings"
	"time"
)

// BeginConversationIdentity starts the host-owned identity for an ordinary
// conversation. It deliberately does not construct an Agency coordinator or
// worker. Work-enabled hosts replace this scope with the coordinator's scope
// through adoptConversationScope below.
func (p *Pipeline) BeginConversationIdentity(sessionID string) error {
	if p == nil {
		return errors.New("pipeline: nil pipeline")
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("pipeline: session id is required")
	}
	p.identityMu.Lock()
	p.identityScope = work.WorkScope{SessionID: sessionID, RuntimeEpoch: newWorkID(), ConversationEpoch: newWorkID()}
	p.traceOrigin = time.Now()
	p.identityRetired = false
	p.identityMu.Unlock()
	return nil
}

// ConversationScope returns the current identity for selection and snapshots.
// A live work coordinator is authoritative; the local scope is used only when
// background work is disabled.
func (p *Pipeline) ConversationScope() work.WorkScope {
	if p == nil {
		return work.WorkScope{}
	}
	if p.Agency != nil {
		if snapshot := p.WorkSnapshot(); snapshot.Scope.Valid() {
			return snapshot.Scope
		}
	}
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.identityScope
}

// ConversationRetired reports whether the current host identity is closed.
func (p *Pipeline) ConversationRetired() bool {
	if p == nil {
		return true
	}
	if p.Agency != nil {
		if snapshot := p.WorkSnapshot(); snapshot.Scope.Valid() {
			return snapshot.Retired
		}
	}
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.identityRetired
}

func (p *Pipeline) adoptConversationScope(scope work.WorkScope) {
	p.identityMu.Lock()
	if p.identityScope.RuntimeEpoch != scope.RuntimeEpoch {
		p.traceOrigin = time.Now()
	}
	p.identityScope = scope
	p.identityRetired = false
	p.identityMu.Unlock()
}

func (p *Pipeline) retireConversationIdentity() {
	p.identityMu.Lock()
	p.identityRetired = true
	p.identityMu.Unlock()
}

// EnableConversationWork binds the Agency to the host-owned conversation
// scope. Persistence remains outside the pipeline through SetWorkObserver.
func (p *Pipeline) EnableConversationWork(sessionID string, saved *work.Snapshot, cfg *config.Config) error {
	if p == nil || p.Agency == nil {
		return fmt.Errorf("pipeline: work agency is unavailable")
	}
	if err := p.Agency.EnableConversationWork(sessionID, saved, cfg, nil); err != nil {
		return err
	}
	p.adoptConversationScope(p.WorkSnapshot().Scope)
	return nil
}
func (p *Pipeline) WorkSnapshot() work.Snapshot {
	if p == nil || p.Agency == nil {
		return work.Snapshot{}
	}
	return p.Agency.WorkSnapshot()
}
func (p *Pipeline) WorkCoordinator() *work.Coordinator {
	if p == nil || p.Agency == nil {
		return nil
	}
	return p.Agency.WorkCoordinator()
}
func (p *Pipeline) SetWorkObserver(fn func(work.Snapshot)) {
	if p != nil && p.Agency != nil {
		p.Agency.SetWorkObserver(fn)
	}
}
func (p *Pipeline) ClearConversation() error {
	if p == nil {
		return fmt.Errorf("pipeline: nil pipeline")
	}
	p.closeCompletionCapture()
	var err error
	if p.Agency != nil && p.WorkSnapshot().Scope.Valid() {
		err = p.Agency.ClearConversation()
		if err == nil {
			p.adoptConversationScope(p.WorkSnapshot().Scope)
		}
	} else if scope := p.ConversationScope(); scope.Valid() {
		// Work-disabled sessions still need an epoch fence for clear/restore.
		next := work.WorkScope{SessionID: scope.SessionID, RuntimeEpoch: newWorkID(), ConversationEpoch: newWorkID()}
		p.adoptConversationScope(next)
	}
	if p.Brain != nil {
		p.Brain.ClearHistory()
	}
	if p.Agency != nil {
		p.Agency.discardPendingSpeech()
	}
	return err
}
func (p *Pipeline) CloseWork() error {
	if p != nil {
		p.closeCompletionCapture()
		if p.Agency != nil {
			p.Agency.discardPendingSpeech()
		}
	}
	if p == nil {
		return nil
	}
	if p.Agency == nil || !p.WorkSnapshot().Scope.Valid() {
		p.retireConversationIdentity()
		return nil
	}
	err := p.Agency.CloseWork()
	p.retireConversationIdentity()
	return err
}

func (p *Pipeline) CancelWork(id string) error {
	if p == nil || p.Agency == nil {
		return work.ErrUnknown
	}
	return p.Agency.CancelWork(id)
}

func (p *Pipeline) ReplaceConversationBrain(next brain.Provider, worker brain.Provider, cfg *config.Config, workerErr error) error {
	if p == nil || p.Agency == nil {
		return fmt.Errorf("pipeline: work agency is unavailable")
	}
	prepared, err := p.PrepareConversationBrain(next, worker, cfg, workerErr)
	if err != nil {
		return err
	}
	return prepared.Commit(nil)
}

func (p *Pipeline) emitWorkSnapshot(snapshot work.Snapshot) {
	p.emit(events.WorkStateChanged{Snapshot: snapshot})
}
