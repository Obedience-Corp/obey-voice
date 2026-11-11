package personaruntime

import (
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// transcript is the canonical conversation while the live provider keeps no
// shared history of its own. Only an explicit clear or restore replaces its
// turns; a turn recorded for a conversation that is no longer current is
// refused rather than added to the next one. It also holds, for personas whose
// provider keeps its own session, how much of the conversation that session
// holds; a clear drops those cursors with the turns.
type transcript struct {
	mu       sync.Mutex
	current  func() string
	turns    []brain.Turn
	sessions brain.PersonaContexts
}

func newTranscript(current func() string, turns []brain.Turn) *transcript {
	return &transcript{current: current, turns: canonicalTurns(turns)}
}

// reset replaces the turns. Session cursors survive; each is checked against
// the turns before use.
func (t *transcript) reset(turns []brain.Turn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns = canonicalTurns(turns)
}

func (t *transcript) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns, t.sessions = nil, nil
}

func (t *transcript) Turns() []brain.Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return brain.CloneTurns(t.turns)
}

func (t *transcript) Append(epoch string, turns ...brain.Turn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if epoch != t.current() {
		return errors.New("transcript: turn belongs to a replaced conversation")
	}
	t.turns = append(t.turns, canonicalTurns(turns)...)
	return nil
}

// CatchUp returns what personaID's provider session may receive and the
// transcript length it covers. It reports false when the persona has no
// cursor that still matches the transcript. Only spoken lines are shared;
// another persona's tool calls and tool output stay on this machine.
func (t *transcript) CatchUp(personaID string) (brain.SessionCatchUp, int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cursor, ok := t.sessions[personaID]
	if !ok || cursor.Session == nil || !cursor.Matches(t.turns) {
		return brain.SessionCatchUp{}, 0, false
	}
	var catchUp brain.SessionCatchUp
	for i := cursor.Session.From; i < len(t.turns); i++ {
		turn := t.turns[i]
		if turn.Role == "tool" || (turn.Role == "assistant" && strings.TrimSpace(turn.Content) == "") {
			continue
		}
		if i < cursor.Through {
			catchUp.Seen++
		}
		catchUp.Turns = append(catchUp.Turns, turn)
	}
	catchUp.Turns = brain.CloneTurns(catchUp.Turns)
	return catchUp, len(t.turns), true
}

// AppendSeen records a completed request of personaID whose catch-up covered
// the transcript up to covered. Its session then holds these turns too,
// unless other turns were recorded after covered, which it has not seen.
func (t *transcript) AppendSeen(epoch, personaID string, covered int, turns ...brain.Turn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if epoch != t.current() {
		return errors.New("transcript: turn belongs to a replaced conversation")
	}
	recorded := len(t.turns)
	t.turns = append(t.turns, canonicalTurns(turns)...)
	cursor, ok := t.sessions[personaID]
	if !ok || cursor.Session == nil || covered < cursor.Through || covered > recorded {
		return nil
	}
	through := covered
	if recorded == covered {
		through = len(t.turns)
	}
	t.sessions[personaID] = brain.SessionContext(t.turns, cursor.Session.From, through)
	return nil
}

// session returns personaID's cursor when it still matches turns.
func (t *transcript) session(personaID string, turns []brain.Turn) (brain.PersonaContext, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cursor, ok := t.sessions[personaID]
	return cursor, ok && cursor.Session != nil && cursor.Matches(turns)
}

// setLive records the selected persona with its cursor when its provider
// keeps a session, or removes any cursor it had when cursor is nil.
func (t *transcript) setLive(personaID string, cursor *brain.PersonaContext) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cursor == nil {
		delete(t.sessions, personaID)
		return
	}
	if t.sessions == nil {
		t.sessions = make(brain.PersonaContexts)
	}
	t.sessions[personaID] = *cursor
}

// restoreSessions replaces every session cursor with those in contexts.
func (t *transcript) restoreSessions(contexts brain.PersonaContexts) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions = make(brain.PersonaContexts)
	for id, state := range contexts.Clone() {
		if state.Session != nil {
			t.sessions[id] = state
		}
	}
}

// addSessions copies the session cursors other than except's into contexts.
func (t *transcript) addSessions(contexts brain.PersonaContexts, except string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, state := range t.sessions.Clone() {
		if id != except {
			contexts[id] = state
		}
	}
}

// SetReplyMedia moves a reply's images into attachments, as the providers do
// for their own replies.
func (t *transcript) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.turns) - 1; i >= 0; i-- {
		if t.turns[i].Role == "assistant" && len(t.turns[i].ToolCalls) == 0 {
			t.turns[i].Content = text
			t.turns[i].Attachments = slices.Clone(refs)
			return nil
		}
	}
	return errors.New("transcript: no assistant reply to attach images to")
}

// sessionView is the part of the transcript loaded into a provider without
// shared history: the window it keeps after a live turn, so a long restored
// conversation does not look like an overdue compaction.
func sessionView(turns []brain.Turn, cfg *config.Config) []brain.Turn {
	if cfg == nil || cfg.MaxHistory <= 0 || len(turns) <= cfg.MaxHistory {
		return turns
	}
	return brain.HistoryWindow(turns, cfg.MaxHistory)
}

// canonicalTurns copies turns into the shared role scheme. Prompt-based
// providers saved replies under their own role name.
func canonicalTurns(turns []brain.Turn) []brain.Turn {
	out := brain.CloneTurns(turns)
	for i := range out {
		if out[i].Role == "samantha" {
			out[i].Role = "assistant"
		}
	}
	return out
}
