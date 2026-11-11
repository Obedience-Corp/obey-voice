package personaruntime

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// sessionShareStart is where a provider keeping its own session begins to
// receive this conversation: the request that first addressed its persona.
// A persona addressed only now starts at the end of the transcript, or at the
// reply that handed it the floor. Earlier turns are never sent to that
// provider. Returning 0 here would share the whole conversation instead.
func sessionShareStart(turns []brain.Turn, personaID string, handoff bool) int {
	for i, turn := range turns {
		if turn.Role != "assistant" || turn.PersonaID != personaID {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if turns[j].Role == "user" {
				return j
			}
		}
		return i
	}
	if handoff && len(turns) > 0 && turns[len(turns)-1].Role == "assistant" {
		return len(turns) - 1
	}
	return len(turns)
}

// sessionCursor returns the persona's cursor when it still matches turns,
// otherwise a new one that has seen nothing from its share start.
func (s *Selector) sessionCursor(personaID string, turns []brain.Turn, handoff bool) brain.PersonaContext {
	if cursor, ok := s.transcript.session(personaID, turns); ok {
		return cursor
	}
	from := sessionShareStart(turns, personaID, handoff)
	return brain.SessionContext(turns, from, from)
}

// withSessions returns contexts with every persona's session cursor added for
// persistence. The cursor for personaID is replaced by cursor, or left out
// when cursor is nil.
func (s *Selector) withSessions(contexts brain.PersonaContexts, personaID string, cursor *brain.PersonaContext) brain.PersonaContexts {
	out := contexts.Clone()
	s.transcript.addSessions(out, personaID)
	if cursor != nil {
		out[personaID] = *cursor
	}
	return out
}

// replayContexts drops session cursors, which the transcript owns.
func replayContexts(contexts brain.PersonaContexts) brain.PersonaContexts {
	out := contexts.Clone()
	for id, state := range out {
		if state.Session != nil {
			delete(out, id)
		}
	}
	return out
}
