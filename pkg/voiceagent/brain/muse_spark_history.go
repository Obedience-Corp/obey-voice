package brain

import (
	"fmt"
	"slices"
	"strings"
)

// History is the transcript authority for persistence. ContextHistory is only
// the bounded model view; trimming and compaction must never rewrite History.
func (b *MuseSparkBrain) History() []Turn          { return deepCopyTurns(b.canonical) }
func (b *MuseSparkBrain) CanonicalHistory() []Turn { return b.History() }
func (b *MuseSparkBrain) ContextHistory() []Turn   { return deepCopyTurns(b.working) }
func (b *MuseSparkBrain) ClearHistory() {
	b.canonical, b.working = nil, nil
	b.snapshot = ContinuitySnapshot{}
}
func (b *MuseSparkBrain) LoadHistory(turns []Turn) {
	b.ClearHistory()
	_ = b.ImportHistory(turns)
}
func (b *MuseSparkBrain) ImportHistory(turns []Turn) error {
	b.canonical = deepCopyTurns(turns)
	b.working = deepCopyTurns(turns)
	b.trimWorking()
	return nil
}
func (b *MuseSparkBrain) ReseedContinuity(turns []Turn) {
	b.working = deepCopyTurns(turns)
	b.trimWorking()
}
func (b *MuseSparkBrain) ContinuitySnapshot() ContinuitySnapshot {
	s := b.snapshot
	s.Canonical, s.Working = b.History(), b.ContextHistory()
	s.Archive = slices.Clone(s.Archive)
	return s
}
func (b *MuseSparkBrain) ImportContinuity(s ContinuitySnapshot) error {
	b.canonical = deepCopyTurns(s.Canonical)
	working := s.Working
	// A non-nil empty view deliberately excludes the entire transcript.
	if working == nil {
		working = s.Canonical
	}
	b.working = deepCopyTurns(working)
	b.snapshot = ContinuitySnapshot{Archive: slices.Clone(s.Archive), ContextHighWater: s.ContextHighWater, Speakers: slices.Clone(s.Speakers)}
	b.trimWorking()
	return nil
}
func (b *MuseSparkBrain) appendTurn(t Turn) {
	b.canonical = append(b.canonical, t)
	b.working = append(b.working, t)
	b.trimWorking()
}
func (b *MuseSparkBrain) trimWorking() {
	if b.cfg == nil || b.cfg.MaxHistory <= 0 {
		return
	}
	users := 0
	for i := len(b.working) - 1; i >= 0; i-- {
		if b.working[i].Role != "user" {
			continue
		}
		users++
		if users == b.cfg.MaxHistory {
			if i > 0 {
				seed := b.working[0]
				tail := deepCopyTurns(b.working[i:])
				// Keep the shared compaction summary before complete recent exchanges,
				// including any tool results and host-authored continuations.
				if seed.Role == "assistant" && seed.PersonaID == "" && seed.PersonaDisplayName == "" && len(seed.ToolCalls) == 0 {
					tail = append(deepCopyTurns([]Turn{seed}), tail...)
				}
				b.working = tail
			}
			return
		}
	}
}
func (b *MuseSparkBrain) Fresh() Provider {
	return &MuseSparkBrain{client: b.client, cfg: b.cfg.Clone(), personaPrompt: b.personaPrompt, systemPrompt: b.systemPrompt, turnInstruction: b.turnInstruction,
		personaReloader: b.personaReloader, turnReloader: b.turnReloader, speakerNames: b.speakerNames}
}
func (b *MuseSparkBrain) AppendAssistantResponse(text string) error {
	return b.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}
func (b *MuseSparkBrain) AppendAssistantResponseWithIdentity(text string, identity TurnIdentity) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	b.appendTurn(attributedHostTurn(text, identity))
	return nil
}
func (b *MuseSparkBrain) SessionInfo() SessionState {
	return SessionState{Kind: SessionKindChat, Turns: len(b.canonical)}
}

var _ CanonicalHistoryProvider = (*MuseSparkBrain)(nil)
var _ ContextHistoryProvider = (*MuseSparkBrain)(nil)
var _ HistoryProjectionProvider = (*MuseSparkBrain)(nil)
var _ ContinuityTransferProvider = (*MuseSparkBrain)(nil)
