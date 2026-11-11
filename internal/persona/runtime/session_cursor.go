package personaruntime

import (
	"slices"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// sessionShareStart is where a provider keeping its own session begins to
// receive this conversation: the request or handoff that first addressed its
// persona, just before its first reply. A persona addressed only now starts at
// the end of the transcript, or at the reply that handed it the floor. Earlier
// turns are never sent to that provider. Returning 0 here would share the
// whole conversation instead.
func sessionShareStart(turns []brain.Turn, personaID string, handoff bool) int {
	if i := slices.IndexFunc(turns, answeredBy(personaID)); i >= 0 {
		return max(i-1, 0)
	}
	if handoff && len(turns) > 0 && turns[len(turns)-1].Role == "assistant" {
		return len(turns) - 1
	}
	return len(turns)
}

func answeredBy(personaID string) func(brain.Turn) bool {
	return func(turn brain.Turn) bool { return turn.Role == "assistant" && turn.PersonaID == personaID }
}

// sessionCursor returns the persona's cursor when it still matches turns,
// otherwise a new one that has seen nothing from its share start. A cursor
// whose persona has neither received nor answered anything belongs to a
// persona selected but never addressed, so it starts again where it is.
func (s *Selector) sessionCursor(personaID string, turns []brain.Turn, handoff bool) brain.PersonaContext {
	if cursor, ok := s.transcript.session(personaID, turns); ok {
		if from := cursor.Session.From; cursor.Through > from || slices.ContainsFunc(turns[from:], answeredBy(personaID)) {
			return cursor
		}
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
