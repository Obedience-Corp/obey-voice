package personaruntime

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func captureContext(contexts brain.PersonaContexts, id string, provider brain.Provider) {
	if id == "" {
		return
	}
	if owner, ok := provider.(brain.ContinuityTransferProvider); ok {
		contexts[id] = brain.CapturePersonaContext(owner.ContinuitySnapshot())
	}
}

// ContextSnapshots is called at the host's serialized save boundary, after the
// active request joins. It includes updates since the most recent selection.
func (s *Selector) ContextSnapshots() brain.PersonaContexts {
	s.mu.RLock()
	contexts, id, restoring := s.contexts.Clone(), s.selected.PersonaID, s.requiresSelection
	epoch := s.selected.ConversationEpoch
	s.mu.RUnlock()
	if epoch != s.opts.Pipeline.ConversationScope().ConversationEpoch {
		contexts = make(brain.PersonaContexts)
	}
	if !restoring {
		if live := s.opts.Pipeline.Brain; brain.HostedSharedHistory(live) {
			s.refreshShare(id, live)
		} else {
			captureContext(contexts, id, live)
		}
	}
	s.transcript.addSessions(contexts, "")
	return contexts
}

// RestoreContexts follows RestoreSession at the same serialized boundary.
func (s *Selector) RestoreContexts(contexts brain.PersonaContexts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts = replayContexts(contexts)
	s.transcript.restoreSessions(contexts)
}
func (s *Selector) persistContexts(turns []brain.Turn, selected events.PersonaSelection, contexts brain.PersonaContexts) error {
	if s.opts.PersistContexts != nil {
		return s.opts.PersistContexts(turns, selected, contexts.Clone())
	}
	return s.opts.Persist(turns, selected)
}
