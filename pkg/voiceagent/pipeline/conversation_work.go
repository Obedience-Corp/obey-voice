package pipeline

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// EnableConversationWork binds the Agency to the host-owned conversation
// scope. Persistence remains outside the pipeline through SetWorkObserver.
func (p *Pipeline) EnableConversationWork(sessionID string, saved *work.Snapshot, cfg *config.Config) error {
	if p == nil || p.Agency == nil {
		return fmt.Errorf("pipeline: work agency is unavailable")
	}
	return p.Agency.EnableConversationWork(sessionID, saved, cfg, nil)
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
	if p.Agency != nil {
		err = p.Agency.ClearConversation()
	}
	if p.Brain != nil {
		p.Brain.ClearHistory()
	}
	return err
}
func (p *Pipeline) CloseWork() error {
	if p != nil {
		p.closeCompletionCapture()
	}
	if p == nil || p.Agency == nil {
		return nil
	}
	return p.Agency.CloseWork()
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
