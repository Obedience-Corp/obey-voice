package textclean

import "strings"

// Reasoning models emit chain-of-thought as <think>…</think> in the reply
// body; it is for the operator, not the listener. Nesting is unsupported by
// design — the first close ends the region (a depth counter would turn a stray
// close, which these models do emit, into a region that never ends), and a
// close tag outside any region is dropped rather than spoken as "less than
// slash think".
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

// ContainsThinkTag reports whether s still carries think markup — either
// spelling, open or close.
//
// Detection, not filtering: this is the question a leak check asks after
// ThinkFilter has run, and it lives here so the caller does not keep its own
// idea of what a think tag looks like. Contains(s, "<think") — the obvious
// hand-rolled version — misses a stray "</think>", which is exactly the shape
// these models emit and the filter drops silently.
func ContainsThinkTag(s string) bool {
	lower := strings.ToLower(s)
	for _, tags := range [][]string{thinkOpenTags, thinkCloseTags} {
		for _, tag := range tags {
			if strings.Contains(lower, tag) {
				return true
			}
		}
	}
	return false
}

// SplitThink separates a reply into chain-of-thought (inner text, no tags)
// and the spoken body; an unclosed <think> at the end is still think, so a
// live stream can paint it before </think> arrives. Two empty results from
// non-blank input mean the reply is only think markup so far — callers must
// not fall back to rendering the raw input. Tag matching mirrors ThinkFilter
// (render path and speech path must agree), including holding a trailing
// partial tag.
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
	out, _, _, _ := f.scan(s)
	return out
}

// FeedSplit is Feed plus the inner think text dropped this call (no tags).
func (f *ThinkFilter) FeedSplit(s string) (speakable, think string) {
	speakable, think, _, _ = f.scan(s)
	return
}

// ContentOffset advances the filter over s exactly as Feed does, but reports
// where the reply proper begins instead of removing the reasoning: the index
// in s of the first non-whitespace byte Feed would have emitted, or len(s)
// when s holds nothing judgeable yet. Whitespace never starts the reply —
// counting it did, and a "\n" before <think> re-opened the label leak.
// A tag prefix held from the previous call was already forwarded, so a first
// content byte landing inside it clamps to 0.
func (f *ThinkFilter) ContentOffset(s string) int {
	held := len(f.hold)
	_, _, first, found := f.scan(s)
	if !found {
		return len(s)
	}
	if i := first - held; i > 0 {
		return i
	}
	return 0
}

// scan is the one tag-matching pass Feed and ContentOffset share. It returns
// the speakable text, the inner think text (no tags), the index of the first
// non-whitespace speakable byte within the scanned string (held prefix
// prepended), and whether such a byte was found.
func (f *ThinkFilter) scan(s string) (speakable, think string, first int, found bool) {
	if f.hold != "" {
		s = f.hold + s
		f.hold = ""
	}
	if s == "" {
		return "", "", 0, false
	}
	var out, thinkB strings.Builder
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
			// Stray close outside a region: dropped, per the package rule.
			if n := matchTagAt(lower, i, thinkCloseTags); n > 0 {
				i += n
				continue
			}
			if s[i] == '<' && isThinkTagPrefix(lower[i:]) {
				f.hold = s[i:]
				break
			}
			if !found && !isSpaceByte(s[i]) {
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
		thinkB.WriteByte(s[i])
		i++
	}
	return out.String(), thinkB.String(), first, found
}

// Flush releases the suffix Feed is still holding; call it after the turn's
// last segment or that text is dropped permanently. A lone "<" at EOF is a
// comparison and is returned; a longer tag fragment ("<th") was never finished
// and is dropped.
func (f *ThinkFilter) Flush() string {
	speakable, _ := f.FlushSplit()
	return speakable
}

// FlushSplit is Flush plus any held inner think that is not a tag fragment.
func (f *ThinkFilter) FlushSplit() (speakable, think string) {
	hold := f.hold
	f.hold = ""
	if hold == "" || looksLikeIncompleteThinkTag(hold) {
		return "", ""
	}
	if f.inThink {
		return "", ""
	}
	return hold, ""
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
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
