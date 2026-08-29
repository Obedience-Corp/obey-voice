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
//
// Nesting is not supported: the first </think> closes the region regardless of
// how many opens preceded it. Qwen3 and the other models this targets emit one
// well-formed pair, and a depth counter would turn a stray close tag — which
// these models do emit — into a region that never ends.
//
// The corollary is that a close tag outside any region is dropped rather than
// passed through as prose. Without depth, an extra close carries no meaning a
// listener wants: it is either the model's own noise or the tail of a nested
// block, and both read aloud as "less than slash think". Losing the tag from a
// sentence genuinely about XML is the cheaper mistake.
var (
	thinkOpenTags  = []string{"<think>", "<thinking>"}
	thinkCloseTags = []string{"</think>", "</thinking>"}
)

// StripThinkBlocks removes complete and unclosed <think>/<thinking> regions
// from a whole reply. Safe on a finished string; do not use on a partial
// stream (an unclosed open would eat the spoken reply that has not arrived).
func StripThinkBlocks(s string) string {
	var f ThinkFilter
	out := f.Feed(s) + f.Flush()
	if out == s {
		return s
	}
	return repairSpacing(out)
}

// SplitThink separates a reply into chain-of-thought (inner text, no tags)
// and the spoken body. An unclosed <think> at the end is still think, so a
// live stream can paint it before </think> arrives.
//
// Only tag bytes are removed and both halves are trimmed, so two empty results
// from non-blank input mean the reply is nothing but think markup so far —
// the frame between "<think>" arriving and the first reasoning token. Callers
// must not fall back to rendering the input in that case; that is the raw tag
// on screen.
//
// It matches tags by the same rules ThinkFilter does, deliberately: this is the
// render path and that is the speech path, and a reader comparing the bubble to
// what was said must not find them disagreeing about what a tag is. That
// includes holding back a trailing partial tag — a frame landing mid-"<think>"
// paints nothing rather than the bytes so far.
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
			if n := matchTagAt(lower, i, thinkCloseTags); n > 0 {
				i += n
				continue
			}
			if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
				break
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
		if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
			break
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
	out, _, _ := f.scan(s)
	return out
}

// ContentOffset advances the filter over s exactly as Feed does, but reports
// where the reply proper begins instead of removing the reasoning: the index in
// s of the first byte Feed would have emitted, or len(s) when s is nothing but
// think markup.
//
// It exists for callers that must forward the model's raw bytes — the TUI
// paints the reasoning live — while still knowing which bytes are the reply and
// therefore judgeable (see brain.labelStripper). A tag prefix held from the
// previous call belongs to that earlier chunk, already forwarded, so a first
// content byte landing inside it clamps to 0.
func (f *ThinkFilter) ContentOffset(s string) int {
	held := len(f.hold)
	out, first, _ := f.scan(s)
	if out == "" {
		return len(s)
	}
	if i := first - held; i > 0 {
		return i
	}
	return 0
}

// scan is the one tag-matching pass Feed and ContentOffset share. It returns the
// speakable text, the index of its first byte within the scanned string (the
// held prefix, if any, prepended to s), and whether any content was emitted.
func (f *ThinkFilter) scan(s string) (string, int, bool) {
	if f.hold != "" {
		s = f.hold + s
		f.hold = ""
	}
	if s == "" {
		return "", 0, false
	}
	var out strings.Builder
	out.Grow(len(s))
	first, found := 0, false
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if !f.inThink {
			if n := matchTagAt(lower, i, thinkOpenTags); n > 0 {
				f.inThink = true
				i += n
				continue
			}
			// A close tag outside any region is the model's noise or a nested
			// block's tail; dropping it is the documented rule above, and it is
			// what keeps this consistent with the same tag arriving at the very
			// end of a segment, which the hold already drops.
			if n := matchTagAt(lower, i, thinkCloseTags); n > 0 {
				i += n
				continue
			}
			if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
				f.hold = s[i:]
				break
			}
			if !found {
				first, found = i, true
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
	return out.String(), first, found
}

// Flush releases the suffix Feed is still holding and must be called once the
// turn's last segment has arrived — nothing else can release it, so a caller
// that skips Flush drops that text permanently.
//
// A lone "<" at EOF is a comparison, not a tag, and is returned. A longer
// fragment ("<th", "</thinkin") is a tag the model never finished writing:
// speaking it would be gibberish, so it is dropped. Both halves of that rule
// live here rather than at the call sites, so every consumer decides the same
// way — StripThinkBlocks used to carry a copy of it.
func (f *ThinkFilter) Flush() string {
	hold := f.hold
	f.hold = ""
	if hold == "" || f.inThink || looksLikeIncompleteThinkTag(hold) {
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
