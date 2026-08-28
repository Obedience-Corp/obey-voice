package textclean

import "strings"

// Qwen3 and similar models dump chain-of-thought as XML in the reply body:
//
//	<think>The user said hi. I should greet them.</think>
//	Hello there!
//
// That block is for the operator, not the listener. StripThinkBlocks removes
// it from a complete string. ThinkFilter does the same across streamed
// sentences so a tag that opens in one segment and closes in another cannot
// leak into TTS.
var (
	thinkOpenTags  = []string{"<think>", "<thinking>"}
	thinkCloseTags = []string{"</think>", "</thinking>"}
)

// StripThinkBlocks removes complete and unclosed <think>/<thinking> regions
// from a whole reply. Safe on a finished string; do not use on a partial
// stream (an unclosed open would eat the spoken reply that has not arrived).
func StripThinkBlocks(s string) string {
	var f ThinkFilter
	out := f.Feed(s)
	if leftover := f.Flush(); leftover != "" && !looksLikeIncompleteThinkTag(leftover) {
		out += leftover
	}
	if out == s {
		return s
	}
	return repairSpacing(out)
}

// SplitThink separates a reply into chain-of-thought (inner text, no tags)
// and the spoken body. An unclosed <think> at the end is still think, so a
// live stream can paint it before </think> arrives.
func SplitThink(s string) (think, body string) {
	var thinkB, bodyB strings.Builder
	inThink := false
	i := 0
	lower := strings.ToLower(s)
	for i < len(s) {
		if !inThink {
			if n := matchTagAt(lower, i, thinkOpenTags); n > 0 {
				inThink = true
				i += n
				continue
			}
			bodyB.WriteByte(s[i])
			i++
			continue
		}
		if n := matchTagAt(lower, i, thinkCloseTags); n > 0 {
			inThink = false
			i += n
			continue
		}
		thinkB.WriteByte(s[i])
		i++
	}
	return strings.TrimSpace(thinkB.String()), strings.TrimSpace(bodyB.String())
}

// ThinkFilter strips think regions from a streaming sentence feed. Keep one
// per turn: state spans segments the way a voice-gate fence does.
type ThinkFilter struct {
	inThink bool
	hold    string
}

// InThink reports whether the filter is currently inside a think region.
func (f *ThinkFilter) InThink() bool { return f.inThink }

// Feed returns the speakable portion of s. Think content is dropped.
func (f *ThinkFilter) Feed(s string) string {
	if f.hold != "" {
		s = f.hold + s
		f.hold = ""
	}
	if s == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(s))
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if !f.inThink {
			if n := matchTagAt(lower, i, thinkOpenTags); n > 0 {
				f.inThink = true
				i += n
				continue
			}
			if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
				f.hold = s[i:]
				break
			}
			out.WriteByte(s[i])
			i++
			continue
		}
		if n := matchTagAt(lower, i, thinkCloseTags); n > 0 {
			f.inThink = false
			i += n
			continue
		}
		if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
			f.hold = s[i:]
			break
		}
		i++
	}
	return out.String()
}

// Flush emits a held suffix that is not inside a think region. A lone "<" at
// EOF is a comparison, not a tag; call this at end of stream.
func (f *ThinkFilter) Flush() string {
	hold := f.hold
	f.hold = ""
	if hold == "" || f.inThink {
		return ""
	}
	return hold
}

func looksLikeIncompleteThinkTag(s string) bool {
	sl := strings.ToLower(s)
	if sl == "<" {
		return false
	}
	for _, tag := range thinkOpenTags {
		if strings.HasPrefix(tag, sl) {
			return true
		}
	}
	for _, tag := range thinkCloseTags {
		if strings.HasPrefix(tag, sl) {
			return true
		}
	}
	return false
}

func matchTagAt(lower string, i int, tags []string) int {
	rest := lower[i:]
	for _, tag := range tags {
		if strings.HasPrefix(rest, tag) {
			return len(tag)
		}
	}
	return 0
}

func isThinkTagPrefix(s string) bool {
	if s == "" || s[0] != '<' {
		return false
	}
	for _, tag := range thinkOpenTags {
		if strings.HasPrefix(tag, s) {
			return true
		}
	}
	for _, tag := range thinkCloseTags {
		if strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}
