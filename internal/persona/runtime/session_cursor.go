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
// never sent. The exceptions are a conversation the persona inherits (see
// inheritedConversation) and the user's choice to share the whole
// conversation; either way it receives the conversation from the start.
func sessionShareStart(turns []brain.Turn, handoff, inherited, whole bool) int {
	switch {
	case inherited || whole:
		return 0
	case handoff && len(turns) > 0 && turns[len(turns)-1].Role == "assistant":
		return len(turns) - 1
	}
	return len(turns)
}

// cursorProvider is the backend a cursor belongs to. A cursor saved before
// cursors named one is Claude's, the only backend that kept them then.
func cursorProvider(cursor brain.PersonaContext) string {
	if cursor.Session.Provider == "" {
		return "claude"
	}
	return cursor.Session.Provider
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

// sessionCursor returns the persona's cursor on provider when it still
// matches turns, otherwise a new one that has seen nothing from its share
// start. A cursor whose persona has neither received nor answered anything
// belongs to a persona selected but never addressed, so it starts again where
// it is; so does one from another backend.
func (s *Selector) sessionCursor(personaID, provider string, turns []brain.Turn, handoff bool) brain.PersonaContext {
	if cursor, ok := s.transcript.session(personaID, turns); ok && cursorProvider(cursor) == provider {
		if from := cursor.Session.From; cursor.Through > from || slices.ContainsFunc(turns[from:], answeredBy(personaID)) {
			return cursor
		}
	}
	from := sessionShareStart(turns, handoff, s.restored.inheritedConversation(personaID, provider, turns), s.sharesWholeConversation())
	return brain.SessionContext(turns, brain.SessionCursor{From: from, Provider: provider}, from)
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

// shareView is what a provider keeping the shared history on a hosted backend
// holds for its persona: every turn, and as the view it sends only the
// persona's share. A view it held before is kept as it was; turns recorded
// since are added as their spoken lines.
func shareView(cursor brain.PersonaContext, turns []brain.Turn) brain.ContinuitySnapshot {
	start := cursor.Session.From
	view := brain.ContinuitySnapshot{Working: []brain.Turn{}}
	if cursor.Working != nil {
		held := cursor
		held.Session = nil
		view = held.Reconcile(turns[:cursor.Through])
		start = cursor.Through
	}
	for _, t := range turns[start:] {
		if shared, ok := hostedTurn(t); ok {
			view.Working = append(view.Working, shared)
		}
	}
	view.Canonical = brain.CloneTurns(turns)
	return view
}

// heldShare is cursor with the view provider now holds.
func heldShare(cursor brain.PersonaContext, provider brain.Provider) brain.PersonaContext {
	snapshot := provider.(brain.ContinuityTransferProvider).ContinuitySnapshot()
	held := brain.SessionContext(snapshot.Canonical, *cursor.Session, len(snapshot.Canonical))
	held.Working = snapshot.Working
	if held.Working == nil {
		held.Working = []brain.Turn{}
	}
	held.Archive, held.ContextHighWater = snapshot.Archive, snapshot.ContextHighWater
	return held
}

// refreshShare records the view a live provider keeping the shared history on
// a hosted backend holds for personaID, so its cursor is saved and taken up
// again with it.
func (s *Selector) refreshShare(personaID string, provider brain.Provider) {
	if cursor, ok := s.transcript.session(personaID, provider.History()); ok {
		held := heldShare(cursor, provider)
		s.transcript.setLive(personaID, &held)
	}
}

// LoadHistory loads turns into the live provider for a host that restores or
// replaces the conversation outside selection. A provider keeping its own
// session holds only what its persona may receive of turns, and nothing
// unless its session has received part of them: only then does the cursor's
// prefix show that turns are this persona's conversation. A provider keeping
// the shared history on a hosted backend keeps every turn but sends only
// that share.
func (s *Selector) LoadHistory(turns []brain.Turn) {
	live := s.opts.Pipeline.Brain
	if brain.HostedSharedHistory(live) {
		cursor, ok := s.transcript.session(s.Snapshot().PersonaID, turns)
		if !ok || cursor.Through == cursor.Session.From {
			cursor = brain.SessionContext(turns, brain.SessionCursor{From: len(turns)}, len(turns))
		}
		_ = live.(brain.ContinuityTransferProvider).ImportContinuity(shareView(cursor, turns))
		return
	}
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
