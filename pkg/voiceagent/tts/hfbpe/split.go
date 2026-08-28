package hfbpe

import (
	"fmt"
	"unicode/utf8"
)

// splitBehavior mirrors tokenizers' SplitDelimiterBehavior. Only the two the
// Audio8 tokenizer declares are implemented; loadPreTokenizer rejects the rest
// by name rather than approximating them.
type splitBehavior int

const (
	behaviorIsolated splitBehavior = iota
	behaviorContiguous
)

func parseBehavior(name string) (splitBehavior, error) {
	switch name {
	case "Isolated":
		return behaviorIsolated, nil
	case "Contiguous":
		return behaviorContiguous, nil
	case "Removed", "MergedWithPrevious", "MergedWithNext":
		return 0, fmt.Errorf("split behavior %q is not implemented", name)
	default:
		return 0, fmt.Errorf("unknown split behavior %q", name)
	}
}

// matcher locates the next match at or after a byte offset.
type matcher interface {
	find(s string, from int) (start, end int, ok bool)
}

type span struct {
	start, end int
	isMatch    bool
}

// findMatches covers s with alternating non-match/match spans, the same way
// tokenizers' Pattern::find_matches does. An empty match is kept as a
// zero-width split point (and the scan advances one rune) so that it separates
// the surrounding text exactly as the reference does.
func findMatches(s string, m matcher) []span {
	var out []span
	prev, pos := 0, 0
	for pos <= len(s) {
		start, end, ok := m.find(s, pos)
		if !ok {
			break
		}
		if prev != start {
			out = append(out, span{prev, start, false})
		}
		out = append(out, span{start, end, true})
		prev = end
		if end > start {
			pos = end
			continue
		}
		_, size := utf8.DecodeRuneInString(s[end:])
		if size == 0 {
			break
		}
		pos = end + size
	}
	if prev != len(s) {
		out = append(out, span{prev, len(s), false})
	}
	return out
}

// splitString applies one split step to s, dropping the empty pieces the
// reference drops.
//
// invert swaps which spans count as the delimiter. It cannot change the result
// under either implemented behaviour — Isolated keeps every span and Contiguous
// groups by "same flag as the previous span" — but it is threaded through so a
// tokenizer declaring it is honoured rather than silently ignored.
func splitString(s string, m matcher, b splitBehavior, invert bool) []string {
	spans := findMatches(s, m)
	if invert {
		for i := range spans {
			spans[i].isMatch = !spans[i].isMatch
		}
	}
	if b == behaviorContiguous {
		merged := spans[:0:0]
		for _, sp := range spans {
			if n := len(merged); n > 0 && merged[n-1].isMatch == sp.isMatch {
				merged[n-1].end = sp.end
				continue
			}
			merged = append(merged, sp)
		}
		spans = merged
	}
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		if sp.end > sp.start {
			out = append(out, s[sp.start:sp.end])
		}
	}
	return out
}

// runeMatcher matches single runes satisfying a predicate.
type runeMatcher struct{ ok func(rune) bool }

func (m runeMatcher) find(s string, from int) (int, int, bool) {
	for i := from; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if m.ok(r) {
			return i, i + size, true
		}
		i += size
	}
	return 0, 0, false
}

// literalMatcher backs a Split whose pattern is a plain string.
type literalMatcher struct{ lit string }

func (m literalMatcher) find(s string, from int) (int, int, bool) {
	if m.lit == "" || from > len(s) {
		return 0, 0, false
	}
	for i := from; i+len(m.lit) <= len(s); i++ {
		if s[i:i+len(m.lit)] == m.lit {
			return i, i + len(m.lit), true
		}
	}
	return 0, 0, false
}
