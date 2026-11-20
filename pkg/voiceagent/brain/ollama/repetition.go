package ollama

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// replyPrefixGate holds only while the new response could be a replay. Once
// it diverges, it streams unchanged. Matching is exact apart from whitespace;
// this deliberately does not guess whether paraphrases or quotations are bad.
type replyPrefixGate struct {
	prior   string
	pending strings.Builder
	repeat  bool
}

func NormalizeReplyPrefix(s string) string { return strings.Join(strings.Fields(s), " ") }

func (g *replyPrefixGate) feed(s string) string {
	if g.prior == "" {
		return s
	}
	g.pending.WriteString(s)
	if g.repeat {
		return ""
	}
	text := NormalizeReplyPrefix(g.pending.String())
	if strings.HasPrefix(text, g.prior) {
		g.repeat = true
		return ""
	}
	if strings.HasPrefix(g.prior, text) {
		return ""
	}
	return g.release()
}

func (g *replyPrefixGate) release() string {
	text := g.pending.String()
	g.pending.Reset()
	g.prior = ""
	return text
}

// restatementSkip reports whether soFar is still a restatement of already —
// text this turn already streamed, usually before a tool call. Local models
// often emit the spoken line, fire a phantom tool, then emit the same line
// again. Forwarding the second copy is how Veronica said one sentence twice.
// soFar is compared as speakable text: think tags in a restated Content
// must not hide that it is the same reply.
func restatementSkip(already, soFar string) bool {
	already = strings.TrimSpace(already)
	soFar = strings.TrimSpace(textclean.StripThinkBlocks(soFar))
	if already == "" || soFar == "" {
		return false
	}
	return soFar == already || strings.HasPrefix(already, soFar)
}
