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
func (c *CodexBrain) History() []Turn                    { return deepCopyTurns(c.canonical) }
func (c *CodexBrain) CanonicalHistory() []Turn           { return c.History() }
func (c *CodexBrain) ContextHistory() []Turn             { return deepCopyTurns(c.working) }
func (c *CodexBrain) ClearHistory() {
	c.canonical = nil
	c.working = nil
	c.sessionID = ""
	c.snapshot = ContinuitySnapshot{}
}
func (c *CodexBrain) LoadHistory(turns []Turn) {
	c.ClearHistory()
	c.canonical = deepCopyTurns(turns)
	c.working = deepCopyTurns(turns)
	c.trimWorking()
}

// ImportHistory replaces the shared transcript projection without resetting
// continuity metadata. An imported projection always invalidates the native
// thread, whose cached history can no longer be assumed to match.
func (c *CodexBrain) ImportHistory(turns []Turn) error {
	c.canonical = deepCopyTurns(turns)
	c.working = deepCopyTurns(turns)
	c.sessionID = ""
	c.trimWorking()
	return nil
}
func (c *CodexBrain) ContinuitySnapshot() ContinuitySnapshot {
	s := c.snapshot
	s.Archive = slices.Clone(s.Archive)
	s.Canonical = c.History()
	s.Working = c.ContextHistory()
	return s
}
func (c *CodexBrain) ImportContinuity(s ContinuitySnapshot) error {
	c.LoadHistory(s.Canonical)
	c.snapshot = s
	c.snapshot.Archive = slices.Clone(s.Archive)
	if s.Working != nil {
		c.working = deepCopyTurns(s.Working)
		c.trimWorking()
	}
	return nil
}
func (c *CodexBrain) appendTurn(t Turn) {
	c.canonical = append(c.canonical, t)
	c.working = append(c.working, t)
	c.trimWorking()
}

// ReseedContinuity changes only the model view. Compaction must not replace
// the transcript that is persisted or transferred to another persona.
func (c *CodexBrain) ReseedContinuity(turns []Turn) {
	c.working = deepCopyTurns(turns)
	c.sessionID = ""
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
	c.appendTurn(Turn{Role: "assistant", Content: text, At: time.Now().UTC(), PersonaID: i.PersonaID, PersonaDisplayName: i.PersonaDisplayName, PromptHash: i.PromptHash, ResponseID: i.ResponseID})
	c.sessionID = ""
	return nil
}
func (c *CodexBrain) SessionInfo() SessionState {
	return SessionState{Kind: SessionKindHarness, ID: c.sessionID, Turns: len(c.canonical) / 2}
}

var _ Provider = (*CodexBrain)(nil)
var _ FreshProvider = (*CodexBrain)(nil)
var _ ContinuityTransferProvider = (*CodexBrain)(nil)
var _ PersonaPromptFreezer = (*CodexBrain)(nil)
