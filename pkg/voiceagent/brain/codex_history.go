package brain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

func (c *CodexBrain) Fresh() Provider {
	cfg := c.cfg.Clone()
	return &CodexBrain{client: c.client, cfg: cfg, personaPrompt: c.personaPrompt, systemPrompt: c.systemPrompt, turnInstruction: c.turnInstruction, speakerNames: c.speakerNames}
}
func (c *CodexBrain) FreezePersonaPrompt() string {
	c.personaReloader = nil
	return shortPromptHash(c.personaPrompt)
}
func (c *CodexBrain) SetSpeakerNames(names SpeakerNames) { c.speakerNames = names }
func (c *CodexBrain) History() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return deepCopyTurns(c.canonical)
}
func (c *CodexBrain) CanonicalHistory() []Turn { return c.History() }
func (c *CodexBrain) ContextHistory() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return deepCopyTurns(c.working)
}
func (c *CodexBrain) ClearHistory() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearHistory()
}
func (c *CodexBrain) clearHistory() {
	c.canonical = nil
	c.working = nil
	c.dropThread()
	c.snapshot = ContinuitySnapshot{}
}

// dropThread gives up the native thread; the next request starts a new one.
// The caller holds c.mu.
func (c *CodexBrain) dropThread() {
	c.sessionID, c.unsent = "", 0
}

// abandonThread is dropThread for a caller not holding c.mu.
func (c *CodexBrain) abandonThread() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropThread()
}

func (*CodexBrain) parksSession() {}

func (c *CodexBrain) LoadHistory(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(turns)
}
func (c *CodexBrain) loadHistory(turns []Turn) {
	c.clearHistory()
	c.canonical = deepCopyTurns(turns)
	c.working = deepCopyTurns(turns)
	c.trimWorking()
}

// ImportHistory replaces the shared transcript projection without resetting
// continuity metadata. An imported projection always invalidates the native
// thread, whose cached history can no longer be assumed to match.
func (c *CodexBrain) ImportHistory(turns []Turn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.canonical = deepCopyTurns(turns)
	c.working = deepCopyTurns(turns)
	c.dropThread()
	c.trimWorking()
	return nil
}
func (c *CodexBrain) ContinuitySnapshot() ContinuitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snapshot
	s.Archive = slices.Clone(s.Archive)
	s.Canonical = deepCopyTurns(c.canonical)
	s.Working = deepCopyTurns(c.working)
	return s
}

// ImportContinuity replaces the conversation with s. A live thread is kept
// when s only adds lines to the view it holds, as when the host records a
// reply or brings a parked persona back; its next request sends those lines.
// Any other view starts a new thread.
func (c *CodexBrain) ImportContinuity(s ContinuitySnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	thread, tools, unsent, held := c.sessionID, c.sessionTools, c.unsent, c.working
	c.loadHistory(s.Canonical)
	c.snapshot = s
	c.snapshot.Archive = slices.Clone(s.Archive)
	if s.Working != nil {
		c.working = deepCopyTurns(s.Working)
		if thread != "" && extendsView(held, c.working) {
			c.sessionID, c.sessionTools, c.unsent = thread, tools, unsent+len(c.working)-len(held)
		}
		c.trimWorking()
	}
	return nil
}

// extendsView reports whether working begins with every turn of held.
func extendsView(held, working []Turn) bool {
	if len(working) < len(held) {
		return false
	}
	for i, t := range held {
		if t.ResponseID != working[i].ResponseID || cliHistoryRecord(t, nil) != cliHistoryRecord(working[i], nil) {
			return false
		}
	}
	return true
}
func (c *CodexBrain) appendTurn(t Turn) {
	c.canonical = append(c.canonical, t)
	c.working = append(c.working, t)
	c.trimWorking()
}

// ReseedContinuity changes only the model view. Compaction must not replace
// the transcript that is persisted or transferred to another persona.
func (c *CodexBrain) ReseedContinuity(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.working = deepCopyTurns(turns)
	c.dropThread()
	c.trimWorking()
}
func (c *CodexBrain) trimWorking() {
	if c.cfg.MaxHistory <= 0 {
		return
	}
	users := 0
	for i := len(c.working) - 1; i >= 0; i-- {
		if c.working[i].Role != "user" {
			continue
		}
		users++
		if users == c.cfg.MaxHistory {
			// Keep whole exchanges, including their tool calls/results and host
			// continuations. Retain a leading shared compaction seed as well.
			if i > 0 {
				// A thread cannot be sent lines trimmed away before it saw them.
				if c.unsent > len(c.working)-i {
					c.dropThread()
				}
				seed := c.working[0]
				tail := deepCopyTurns(c.working[i:])
				if seed.Role == "assistant" && seed.PersonaID == "" && seed.PersonaDisplayName == "" && len(seed.ToolCalls) == 0 {
					tail = append(deepCopyTurns([]Turn{seed}), tail...)
				}
				c.working = tail
			}
			return
		}
	}
}
func (c *CodexBrain) AppendAssistantResponse(text string) error {
	return c.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}
func (c *CodexBrain) AppendAssistantResponseWithIdentity(text string, i TurnIdentity) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("codex: empty assistant response")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The live thread is sent this line with its next request.
	if c.sessionID != "" {
		c.unsent++
	}
	c.appendTurn(Turn{Role: "assistant", Content: text, At: time.Now().UTC(), PersonaID: i.PersonaID, PersonaDisplayName: i.PersonaDisplayName, PromptHash: i.PromptHash, ResponseID: i.ResponseID})
	return nil
}
func (c *CodexBrain) SessionInfo() SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SessionState{Kind: SessionKindHarness, ID: c.sessionID, Turns: len(c.canonical) / 2}
}

var _ Provider = (*CodexBrain)(nil)
var _ FreshProvider = (*CodexBrain)(nil)
var _ ContinuityTransferProvider = (*CodexBrain)(nil)
var _ PersonaPromptFreezer = (*CodexBrain)(nil)
