package brain

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// The prompt convention teaches models a "Name: content" line format; they
// mirror it and open replies with their own label, which TTS then speaks and
// history reinforces. These helpers remove exactly one leading agent-name
// label (optionally emphasis-wrapped); the name anywhere else is content.

// agentLabelRE matches a leading "Name:" label. Empty names fall back to
// "Obey", mirroring promptUserLine's default.
func agentLabelRE(agentName string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)^\s*(?:\*\*|__|[*_])?` + regexp.QuoteMeta(effectiveAgentName(agentName)) + `(?:\*\*|__|[*_])?\s*:\s*`)
}

func effectiveAgentName(agentName string) string {
	if name := strings.TrimSpace(agentName); name != "" {
		return name
	}
	return "Obey"
}

// StripAgentLabel removes one leading "AgentName:" label from a complete
// reply. Apply before cleanForVoice, while markdown emphasis is still present.
func StripAgentLabel(agentName, text string) string {
	if loc := agentLabelRE(agentName).FindStringIndex(text); loc != nil {
		return text[loc[1]:]
	}
	return text
}

// stripReplyConventions removes both conventions from a complete reply, think
// block first: the label strip is anchored to the reply's start, and a
// reasoning model writes "<think>…</think>\nObey: hi". Streams cannot use it;
// labelStripper applies the same order chunk by chunk.
func stripReplyConventions(agentName, text string) string {
	return StripAgentLabel(agentName, textclean.StripThinkBlocks(text))
}

// labelStripper strips the label from a streamed reply. Output is held only
// while the opening runes could still become the label, far shorter than
// sentence segmentation already buffers.
type labelStripper struct {
	re    *regexp.Regexp
	lname string
	// think separates the reasoning prologue from the reply: the block is
	// forwarded verbatim (the TUI paints it live) and only the reply after it
	// is judged, or the label following </think> slips through to TTS.
	think      textclean.ThinkFilter
	sawContent bool
	pending    strings.Builder
	decided    bool
}

func newLabelStripper(agentName string) *labelStripper {
	name := effectiveAgentName(agentName)
	return &labelStripper{
		re:    agentLabelRE(name),
		lname: strings.ToLower(name),
	}
}

// Feed accepts the next raw chunk and returns whatever is safe to emit now:
// "" while the head is ambiguous, then each chunk verbatim once decided.
func (s *labelStripper) Feed(chunk string) string {
	if s.decided {
		return chunk
	}
	if !s.sawContent {
		i := s.think.ContentOffset(chunk)
		if i >= len(chunk) {
			// Still the prologue; forward it, nothing to judge yet.
			return chunk
		}
		s.sawContent = true
		// ContentOffset points at the first non-whitespace reply byte; pull the
		// whitespace run before it back into the judged head so the label regex
		// eats it. Whitespace before <think> stops at the tag's '>'.
		for i > 0 && unicode.IsSpace(rune(chunk[i-1])) {
			i--
		}
		if i > 0 {
			return chunk[:i] + s.feedHead(chunk[i:])
		}
	}
	return s.feedHead(chunk)
}

// feedHead runs the label decision over the reply's opening bytes.
func (s *labelStripper) feedHead(chunk string) string {
	s.pending.WriteString(chunk)
	text := s.pending.String()
	if s.couldBeLabel(text) {
		return ""
	}
	return s.release(text)
}

// Flush releases whatever the head still holds; call when the stream ends,
// including on error, so a short reply is never swallowed. Prologue bytes are
// never held.
func (s *labelStripper) Flush() string {
	if s.decided {
		return ""
	}
	return s.release(s.pending.String())
}

func (s *labelStripper) release(text string) string {
	s.decided = true
	s.pending.Reset()
	if loc := s.re.FindStringIndex(text); loc != nil {
		return text[loc[1]:]
	}
	return text
}

// couldBeLabel reports whether text is still a strict prefix of some form the
// label can take; the first rune that breaks the pattern resolves the head.
func (s *labelStripper) couldBeLabel(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return true
	}
	bare := strings.TrimLeft(trimmed, "*_")
	if bare == "" {
		return true
	}
	lower := strings.ToLower(bare)
	if len(lower) < len(s.lname) {
		return strings.HasPrefix(s.lname, lower)
	}
	if !strings.HasPrefix(lower, s.lname) {
		return false
	}
	rest := strings.TrimLeftFunc(strings.TrimLeft(lower[len(s.lname):], "*_"), unicode.IsSpace)
	// Only an unfinished tail keeps the hold; a colon resolves through release,
	// where the regex strips it.
	return rest == ""
}
