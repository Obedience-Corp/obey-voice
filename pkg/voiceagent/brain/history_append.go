package brain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func appendHostText(existing, text string) string {
	joined := existing + "\n\n" + text
	if len(joined) <= 8192 {
		return joined
	}
	marker := "[earlier host output truncated]\n"
	limit := 8192 - len(marker)
	tail := joined[len(joined)-limit:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return marker + tail
}

func (b *Brain) AppendAssistantResponse(text string) error {
	return b.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}

func (b *Brain) AppendAssistantResponseWithIdentity(text string, identity TurnIdentity) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	turn := attributedHostTurn(text, identity)
	b.history = append(b.history, turn)
	b.hostContext = appendHostText(b.hostContext, hostTurnText(turn))
	if b.cfg != nil && b.cfg.MaxHistory > 0 {
		b.trimHistory()
	}
	return nil
}

func (g *GrokBrain) AppendAssistantResponse(text string) error {
	return g.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}

func (g *GrokBrain) AppendAssistantResponseWithIdentity(text string, identity TurnIdentity) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	turn := attributedHostTurn(text, identity)
	g.history = append(g.history, turn)
	g.hostContext = appendHostText(g.hostContext, hostTurnText(turn))
	if g.cfg != nil && g.cfg.MaxHistory > 0 {
		g.trimHistory()
	}
	return nil
}

func (c *CursorBrain) AppendAssistantResponse(text string) error {
	return c.AppendAssistantResponseWithIdentity(text, TurnIdentity{})
}

func (c *CursorBrain) AppendAssistantResponseWithIdentity(text string, identity TurnIdentity) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	turn := attributedHostTurn(text, identity)
	c.history = append(c.history, turn)
	c.hostContext = appendHostText(c.hostContext, hostTurnText(turn))
	if c.cfg != nil && c.cfg.MaxHistory > 0 {
		c.trimHistory()
	}
	return nil
}

var _ HistoryAppender = (*Brain)(nil)
var _ HistoryAppender = (*GrokBrain)(nil)
var _ HistoryAppender = (*CursorBrain)(nil)

// Keep immutable authorship in persisted history and in the supplemental
// context sent to a resumed CLI session. Unattributed legacy output stays plain.
func attributedHostTurn(text string, identity TurnIdentity) Turn {
	return Turn{Role: "assistant", Content: text, At: time.Now().UTC(), PersonaID: identity.PersonaID, PersonaDisplayName: identity.PersonaDisplayName, PromptHash: identity.PromptHash, ResponseID: identity.ResponseID}
}

func hostTurnText(turn Turn) string {
	if turn.PersonaID != "" || turn.PersonaDisplayName != "" {
		return promptUserLine(turn, "", nil)
	}
	return turn.Content
}

var _ IdentityHistoryAppender = (*Brain)(nil)
var _ IdentityHistoryAppender = (*GrokBrain)(nil)
var _ IdentityHistoryAppender = (*CursorBrain)(nil)
