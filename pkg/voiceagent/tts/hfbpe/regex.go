package hfbpe

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The pre-tokenizer regexes in a tokenizer.json are Rust `regex` crate source.
// Go's regexp agrees with it on leftmost-first alternation and on greedy
// quantifiers, so the only work here is bridging two documented differences:
//
//   - Rust's \s, \S, \d and \D are Unicode-aware; Go's are ASCII-only. They are
//     rewritten to explicit Unicode classes below.
//   - Rust supports look-around; Go does not. A branch that needs it is matched
//     by a hand-written matcher registered in lookaroundBranches, and any other
//     construct Go cannot compile fails the load by name.
//
// Anything not covered is a load error: a pre-tokenizer that silently splits
// differently produces silently wrong token ids.

// unicodeSpaceClass is Rust's \s (\p{White_Space}), spelled out rather than
// deferred to \p{Zs} so that a Unicode-table bump in either toolchain cannot
// move the class under us. White_Space has been these 25 code points since
// Unicode 6.3 dropped U+180E.
const unicodeSpaceClass = `\x{09}-\x{0D}\x{20}\x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}`

// lookaroundBranches maps an alternation branch that Go's regexp cannot
// express onto a hand-written matcher with identical semantics.
var lookaroundBranches = map[string]func(s string, pos int) (int, bool){
	`\s+(?!\S)`: matchSpacesNotBeforeNonSpace,
}

// matchSpacesNotBeforeNonSpace implements `\s+(?!\S)` anchored at pos: the
// greedy run of whitespace, backtracked by one rune when a non-space follows
// (which is always, unless the run ends the input).
func matchSpacesNotBeforeNonSpace(s string, pos int) (int, bool) {
	end, last := pos, pos
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if !isUnicodeSpace(r) {
			break
		}
		last = end
		end += size
	}
	if end == pos {
		return 0, false
	}
	if end == len(s) {
		return end, true
	}
	if last == pos {
		return 0, false // a one-rune run cannot give a rune back
	}
	return last, true
}

// isUnicodeSpace is the same set as unicodeSpaceClass, for the hand-written
// branch matchers.
func isUnicodeSpace(r rune) bool {
	switch r {
	case 0x20, 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		return true
	}
	return (r >= 0x09 && r <= 0x0D) || (r >= 0x2000 && r <= 0x200A)
}

// altMatcher reproduces leftmost-first alternation over a pattern's top-level
// branches: the earliest offset where any branch matches wins, and among
// branches at that offset the earliest in source order wins. Consecutive
// Go-compatible branches are compiled into one anchored regexp, which
// preserves that order because Go's alternation is leftmost-first too.
type altMatcher struct {
	steps []altStep
}

type altStep struct {
	re *regexp.Regexp                      // anchored; nil for a hand-written branch
	fn func(s string, pos int) (int, bool) // end offset, ok
}

func (m *altMatcher) matchAt(s string, pos int) (int, bool) {
	for _, st := range m.steps {
		if st.fn != nil {
			if end, ok := st.fn(s, pos); ok {
				return end, true
			}
			continue
		}
		if loc := st.re.FindStringIndex(s[pos:]); loc != nil {
			return pos + loc[1], true
		}
	}
	return 0, false
}

func (m *altMatcher) find(s string, from int) (int, int, bool) {
	for i := from; i <= len(s); {
		if end, ok := m.matchAt(s, i); ok {
			return i, end, true
		}
		if i == len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return 0, 0, false
}

func newAltMatcher(pattern string) (*altMatcher, error) {
	branches, err := splitAlternation(pattern)
	if err != nil {
		return nil, err
	}
	m := &altMatcher{}
	var group []string
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		src := `\A(?:` + strings.Join(group, "|") + `)`
		re, err := regexp.Compile(src)
		if err != nil {
			return fmt.Errorf("compile branch group %q: %w", strings.Join(group, "|"), err)
		}
		m.steps = append(m.steps, altStep{re: re})
		group = nil
		return nil
	}
	for _, raw := range branches {
		if fn, ok := lookaroundBranches[raw]; ok {
			if err := flush(); err != nil {
				return nil, err
			}
			m.steps = append(m.steps, altStep{fn: fn})
			continue
		}
		translated, err := translateBranch(raw)
		if err != nil {
			return nil, fmt.Errorf("branch %q: %w", raw, err)
		}
		if _, err := regexp.Compile(`\A(?:` + translated + `)`); err != nil {
			return nil, fmt.Errorf("branch %q: Go regexp cannot compile it: %w", raw, err)
		}
		group = append(group, translated)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(m.steps) == 0 {
		return nil, fmt.Errorf("pattern %q has no branches", pattern)
	}
	return m, nil
}

// splitAlternation cuts a pattern at its top-level | characters.
func splitAlternation(pattern string) ([]string, error) {
	var out []string
	var depth int
	inClass := false
	start := 0
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '\\':
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced ) in pattern %q", pattern)
			}
		case c == '|' && depth == 0:
			out = append(out, pattern[start:i])
			start = i + 1
		}
	}
	if inClass || depth != 0 {
		return nil, fmt.Errorf("unterminated group or class in pattern %q", pattern)
	}
	return append(out, pattern[start:]), nil
}

// translateBranch rewrites Rust regex source into Go regexp source, rejecting
// every construct whose meaning would not survive the trip.
func translateBranch(src string) (string, error) {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\\' {
			if i+1 >= len(src) {
				return "", fmt.Errorf("trailing backslash")
			}
			esc := src[i+1]
			i++
			switch esc {
			case 's':
				if inClass {
					b.WriteString(unicodeSpaceClass)
				} else {
					b.WriteString("[" + unicodeSpaceClass + "]")
				}
			case 'S':
				if inClass {
					return "", fmt.Errorf(`\S inside a character class needs set subtraction`)
				}
				b.WriteString("[^" + unicodeSpaceClass + "]")
			case 'd':
				b.WriteString(`\p{Nd}`)
			case 'D':
				if inClass {
					return "", fmt.Errorf(`\D inside a character class needs set subtraction`)
				}
				b.WriteString(`[^\p{Nd}]`)
			case 'w', 'W', 'b', 'B', 'A', 'z', 'Z':
				return "", fmt.Errorf(`\%c has different (or no) meaning in Go's regexp`, esc)
			default:
				b.WriteByte(c)
				b.WriteByte(esc)
			}
			continue
		}
		switch {
		case inClass:
			// Character-class set operations are the one family Go's regexp
			// compiles happily and reads differently: to the reference
			// [\p{L}&&\p{Greek}] is an intersection and [a-z--[aeiou]] a
			// difference, while Go sees literal '&' and '-' members. Silently
			// wrong splits are exactly what this translation exists to prevent.
			if c == '[' {
				return "", fmt.Errorf("a nested character class is a set union in the reference and a literal '[' in Go")
			}
			if (c == '&' || c == '-' || c == '~') && i+1 < len(src) && src[i+1] == c {
				return "", fmt.Errorf("character-class set operation %q means literal characters in Go", src[i:i+2])
			}
			if c == ']' {
				inClass = false
			}
			b.WriteByte(c)
		case c == '[':
			inClass = true
			b.WriteByte(c)
		case c == '^' || c == '$':
			return "", fmt.Errorf("anchor %q is not supported: branches are matched at an offset", string(c))
		default:
			b.WriteByte(c)
		}
	}
	if inClass {
		return "", fmt.Errorf("unterminated character class")
	}
	return b.String(), nil
}
