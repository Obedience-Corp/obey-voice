package brain

import (
	"errors"
	"strings"
)

var ErrResponseRepetition = errors.New("repeating text detected; generation stopped")

// Hold only the tail of an exceptionally long prose word. This catches a
// model stuck generating a syllable (e.g. undundund...) without buffering a
// sentence or mistaking repeated prose, lists or code for a broken generation.
type runawayTextGuard struct {
	word      []byte
	sent      int
	codeTicks int
	ticks     int
	stopped   bool
	requested bool
}

func newRunawayTextGuard(input string) runawayTextGuard {
	// Literal repetition may be the task. Keep those requests under the
	// normal output budget instead of guessing that their output is broken.
	for _, word := range strings.Fields(strings.ToLower(input)) {
		switch strings.Trim(word, "\"'.,!?;:()") {
		case "repeat", "reproduce", "recite", "verbatim":
			return runawayTextGuard{requested: true}
		}
	}
	return runawayTextGuard{}
}

func (g *runawayTextGuard) feed(text string) (string, error) {
	if g.requested {
		return text, nil
	}
	if g.stopped {
		return "", ErrResponseRepetition
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b == '`' {
			out.WriteString(g.flush())
			g.ticks++
			out.WriteByte(b)
			continue
		}
		if g.ticks > 0 {
			if g.codeTicks == 0 {
				g.codeTicks = g.ticks
			} else if g.ticks >= g.codeTicks {
				g.codeTicks = 0
			}
			g.ticks = 0
		}
		if g.codeTicks == 0 && (b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z') {
			g.word = append(g.word, b)
			// Ordinary words retain the provider's exact delta boundaries.
			// Start holding only after a word is implausibly long for prose.
			if len(g.word) <= 64 {
				out.WriteByte(b)
				g.sent++
			}
			for period := 1; period <= 16; period++ {
				n := max(192, period*16)
				if len(g.word) < n {
					continue
				}
				tail := g.word[len(g.word)-n:]
				loop := true
				for j := period; j < len(tail); j++ {
					if tail[j] != tail[j-period] {
						loop = false
						break
					}
				}
				if loop {
					g.stopped = true
					g.word = nil
					g.sent = 0
					return out.String(), ErrResponseRepetition
				}
			}
			if len(g.word) > 256 {
				cut := len(g.word) - 192
				out.Write(g.word[g.sent:cut])
				g.word = g.word[cut:]
				g.sent = 0
			}
			continue
		}
		out.WriteString(g.flush())
		out.WriteByte(b)
	}
	return out.String(), nil
}

func (g *runawayTextGuard) flush() string {
	text := string(g.word[g.sent:])
	g.word = nil
	g.sent = 0
	return text
}
