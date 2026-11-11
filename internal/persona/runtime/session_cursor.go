package personaruntime

import (
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// sessionShareStart is where a provider keeping its own session begins to
// receive this conversation: where its persona is addressed while bound to it,
// at the end of the transcript or at the reply that handed it the floor. The
// persona's cursor keeps that point for as long as it stays on the provider;
// lines from before it, including the persona's own on another backend, are
// never sent. The one exception is a conversation the persona inherits (see
// inheritedConversation), which it receives from the start. Returning 0 here
// would share the whole conversation instead.
func sessionShareStart(turns []brain.Turn, handoff, inherited bool) int {
	switch {
	case inherited:
		return 0
	case handoff && len(turns) > 0 && turns[len(turns)-1].Role == "assistant":
		return len(turns) - 1
	}
	return len(turns)
}

// restoredConversation is a saved conversation from before replies carried a
// persona, and the backend its session was created on.
type restoredConversation struct{ personaID, provider string }

// inheritedConversation reports whether personaID, on provider, receives a
// restored conversation from its start: it is the persona the conversation was
// restored with, the session was created on that provider, and every reply
// still carries no persona, so that one responder gave them all.
func (r restoredConversation) inheritedConversation(personaID, provider string, turns []brain.Turn) bool {
	if r.personaID == "" || r.personaID != personaID || r.provider != provider {
		return false
	}
	replied := false
	for _, t := range turns {
		if t.Role != "assistant" && t.Role != "samantha" {
			continue
		}
		if t.PersonaID != "" || t.PersonaDisplayName != "" {
			return false
		}
		replied = true
	}
	return replied
}

func answeredBy(personaID string) func(brain.Turn) bool {
	return func(turn brain.Turn) bool { return turn.Role == "assistant" && turn.PersonaID == personaID }
}

// sessionCursor returns the persona's cursor when it still matches turns,
// otherwise a new one that has seen nothing from its share start. A cursor
// whose persona has neither received nor answered anything belongs to a
// persona selected but never addressed, so it starts again where it is.
func (s *Selector) sessionCursor(personaID, provider string, turns []brain.Turn, handoff bool) brain.PersonaContext {
	if cursor, ok := s.transcript.session(personaID, turns); ok {
		if from := cursor.Session.From; cursor.Through > from || slices.ContainsFunc(turns[from:], answeredBy(personaID)) {
			return cursor
		}
	}
	from := sessionShareStart(turns, handoff, s.restored.inheritedConversation(personaID, provider, turns))
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

// hostedTurn is what a provider keeping its own session may receive of a
// turn: its spoken line. Tool calls, tool output and attachment names stay on
// this machine.
func hostedTurn(t brain.Turn) (brain.Turn, bool) {
	if t.Role == "tool" || strings.TrimSpace(t.Content) == "" {
		return brain.Turn{}, false
	}
	t = brain.CloneTurns([]brain.Turn{t})[0]
	t.ToolCalls, t.ToolName, t.ToolCallID, t.Attachments = nil, "", "", nil
	return t, true
}

// sessionHistory is the history loaded into a provider keeping its own
// session: the spoken lines since its share start, in the window it keeps.
func sessionHistory(turns []brain.Turn, cursor brain.PersonaContext, cfg *config.Config) []brain.Turn {
	var out []brain.Turn
	for _, t := range turns[cursor.Session.From:] {
		if hosted, ok := hostedTurn(t); ok {
			out = append(out, hosted)
		}
	}
	return sessionView(out, cfg)
}

// LoadHistory loads turns into the live provider for a host that restores or
// replaces the conversation outside selection. A provider keeping its own
// session holds only what its persona may receive of turns, and nothing
// unless its session has received part of them: only then does the cursor's
// prefix show that turns are this persona's conversation.
func (s *Selector) LoadHistory(turns []brain.Turn) {
	live := s.opts.Pipeline.Brain
	if !brain.KeepsPersonaSession(live) {
		live.LoadHistory(turns)
		return
	}
	turns = canonicalTurns(turns)
	var held []brain.Turn
	if cursor, ok := s.transcript.session(s.Snapshot().PersonaID, turns); ok && cursor.Through > cursor.Session.From {
		held = sessionHistory(turns, cursor, s.Config())
	}
	live.LoadHistory(held)
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
