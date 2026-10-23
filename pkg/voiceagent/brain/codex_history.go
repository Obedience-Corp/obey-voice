package brain

import (
	"fmt"
	"strings"
	"time"
)

func (c *CodexBrain) Fresh() Provider {
	cfg := c.cfg.Clone()
	return &CodexBrain{client: c.client, cfg: cfg, systemPrompt: c.systemPrompt, turnInstruction: c.turnInstruction, speakerNames: c.speakerNames}
}
func (c *CodexBrain) FreezePersonaPrompt() string {
	c.personaReloader = nil
	return shortPromptHash(c.systemPrompt)
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
func (c *CodexBrain) ImportHistory(turns []Turn) error { c.LoadHistory(turns); return nil }
func (c *CodexBrain) ContinuitySnapshot() ContinuitySnapshot {
	s := c.snapshot
	s.Canonical = c.History()
	s.Working = c.ContextHistory()
	return s
}
func (c *CodexBrain) ImportContinuity(s ContinuitySnapshot) error {
	c.LoadHistory(s.Canonical)
	c.snapshot = s
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
func (c *CodexBrain) trimWorking() {
	if n := c.cfg.MaxHistory * 2; n > 0 && len(c.working) > n {
		c.working = c.working[len(c.working)-n:]
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
