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
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	b.history = append(b.history, Turn{Role: "assistant", Content: text, At: time.Now().UTC()})
	b.hostContext = appendHostText(b.hostContext, text)
	if b.cfg != nil && b.cfg.MaxHistory > 0 {
		b.trimHistory()
	}
	return nil
}

func (g *GrokBrain) AppendAssistantResponse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	g.history = append(g.history, Turn{Role: "samantha", Content: text, At: time.Now().UTC()})
	g.hostContext = appendHostText(g.hostContext, text)
	if g.cfg != nil && g.cfg.MaxHistory > 0 {
		g.trimHistory()
	}
	return nil
}

func (c *CursorBrain) AppendAssistantResponse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	c.history = append(c.history, Turn{Role: "assistant", Content: text, At: time.Now().UTC()})
	c.hostContext = appendHostText(c.hostContext, text)
	if c.cfg != nil && c.cfg.MaxHistory > 0 {
		c.trimHistory()
	}
	return nil
}

var _ HistoryAppender = (*Brain)(nil)
var _ HistoryAppender = (*GrokBrain)(nil)
var _ HistoryAppender = (*CursorBrain)(nil)
