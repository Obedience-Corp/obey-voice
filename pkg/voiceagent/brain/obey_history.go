package brain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

func (c *ObeyBrain) Fresh() Provider {
	cfg := c.cfg.Clone()
	return &ObeyBrain{client: c.client, cfg: cfg, personaPrompt: c.personaPrompt, systemPrompt: c.systemPrompt, turnInstruction: c.turnInstruction, speakerNames: c.speakerNames}
}
func (c *ObeyBrain) FreezePersonaPrompt() string {
	c.personaReloader = nil
	return shortPromptHash(c.personaPrompt)
}
func (c *ObeyBrain) SetSpeakerNames(names SpeakerNames) { c.speakerNames = names }
func (c *ObeyBrain) History() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return deepCopyTurns(c.canonical)
}
func (c *ObeyBrain) CanonicalHistory() []Turn { return c.History() }
func (c *ObeyBrain) ContextHistory() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return deepCopyTurns(c.working)
}
func (c *ObeyBrain) ClearHistory() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(nil)
}
func (c *ObeyBrain) LoadHistory(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(turns)
}
func (c *ObeyBrain) loadHistory(turns []Turn) {
	c.snapshot = ContinuitySnapshot{}
	c.importHistory(turns)
}

// ImportHistory replaces the shared transcript projection without resetting
// continuity metadata.
func (c *ObeyBrain) ImportHistory(turns []Turn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.importHistory(turns)
	return nil
}
func (c *ObeyBrain) importHistory(turns []Turn) {
	c.canonical = deepCopyTurns(turns)
	c.working = deepCopyTurns(turns)
	c.request = nil
	c.trimWorking()
}
func (c *ObeyBrain) ContinuitySnapshot() ContinuitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snapshot
	s.Archive = slices.Clone(s.Archive)
	s.Canonical = deepCopyTurns(c.canonical)
	s.Working = deepCopyTurns(c.working)
	return s
}
func (c *ObeyBrain) ImportContinuity(s ContinuitySnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(s.Canonical)
	c.snapshot = s
	c.snapshot.Archive = slices.Clone(s.Archive)
	if s.Working != nil {
		c.working = deepCopyTurns(s.Working)
		c.trimWorking()
	}
	return nil
}

// appendTurn and trimWorking are called with c.mu held.
func (c *ObeyBrain) appendTurn(t Turn) {
	c.canonical = append(c.canonical, t)
	c.working = append(c.working, t)
	c.trimWorking()
}

// ReseedContinuity changes only the model view. Compaction must not replace
// the transcript that is persisted or transferred to another persona.
func (c *ObeyBrain) ReseedContinuity(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.working = deepCopyTurns(turns)
	c.request = nil
	c.trimWorking()
}
func (c *ObeyBrain) trimWorking() {
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
func (c *ObeyBrain) AppendAssistantResponse(text string) error {
	return c.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}
func (c *ObeyBrain) AppendAssistantResponseWithIdentity(text string, i TurnIdentity) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("obey: empty assistant response")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appendTurn(Turn{Role: "assistant", Content: text, At: time.Now().UTC(), PersonaID: i.PersonaID, PersonaDisplayName: i.PersonaDisplayName, PromptHash: i.PromptHash, ResponseID: i.ResponseID})
	return nil
}
func (c *ObeyBrain) SessionInfo() SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SessionState{Kind: SessionKindChat, Turns: len(c.canonical) / 2}
}

var _ Provider = (*ObeyBrain)(nil)
var _ FreshProvider = (*ObeyBrain)(nil)
var _ ContinuityTransferProvider = (*ObeyBrain)(nil)
var _ PersonaPromptFreezer = (*ObeyBrain)(nil)
