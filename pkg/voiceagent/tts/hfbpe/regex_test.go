package hfbpe

import (
	"slices"
	"strings"
	"testing"
)

// audio8SplitPattern is the Split pattern the checkpoint declares, verbatim.
const audio8SplitPattern = `[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n/]*|\s*[\r\n]+|\s+(?!\S)|\s+`

func TestSplitAlternation(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{"single", `abc`, []string{`abc`}},
		{"two", `a|b`, []string{`a`, `b`}},
		{"pipe in a class", `[a|b]|c`, []string{`[a|b]`, `c`}},
		{"pipe in a group", `(a|b)|c`, []string{`(a|b)`, `c`}},
		{"escaped pipe", `a\|b|c`, []string{`a\|b`, `c`}},
		{"escaped class close", `[\]|]|x`, []string{`[\]|]`, `x`}},
		{"empty branch", `a||b`, []string{`a`, ``, `b`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitAlternation(tc.pattern)
			if err != nil {
				t.Fatalf("splitAlternation(%q) error = %v", tc.pattern, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("splitAlternation(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestSplitAlternationRejectsUnbalanced(t *testing.T) {
	for _, pattern := range []string{`(a`, `a)`, `[a`} {
		if _, err := splitAlternation(pattern); err == nil {
			t.Errorf("splitAlternation(%q) error = nil, want an error", pattern)
		}
	}
}

func TestAudio8PatternSplitsIntoItsSevenBranches(t *testing.T) {
	branches, err := splitAlternation(audio8SplitPattern)
	if err != nil {
		t.Fatalf("splitAlternation: %v", err)
	}
	if len(branches) != 7 {
		t.Fatalf("got %d branches, want 7: %q", len(branches), branches)
	}
	if _, ok := lookaroundBranches[branches[5]]; !ok {
		t.Fatalf("branch 5 = %q, want the hand-implemented %q", branches[5], `\s+(?!\S)`)
	}
	for i, b := range branches {
		if i == 5 {
			continue
		}
		if _, err := translateBranch(b); err != nil {
			t.Errorf("branch %d (%q) does not translate: %v", i, b, err)
		}
	}
}

func TestTranslateBranch(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"passthrough", `\p{L}+`, `\p{L}+`},
		{"space outside a class", `\s+`, `[` + unicodeSpaceClass + `]+`},
		{"space inside a class", `[^\s\p{L}]`, `[^` + unicodeSpaceClass + `\p{L}]`},
		{"non-space", `\S`, `[^` + unicodeSpaceClass + `]`},
		{"digit", `\d+`, `\p{Nd}+`},
		{"escaped backslash keeps its escape", `a\\s`, `a\\s`},
		// A single hyphen is still a range, and an escaped one still a member:
		// only the doubled operators are rejected.
		{"range", `[0-9]`, `[0-9]`},
		{"leading hyphen", `[^-a]`, `[^-a]`},
		{"escaped hyphen before a range", `[a\--b]`, `[a\--b]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := translateBranch(tc.src)
			if err != nil {
				t.Fatalf("translateBranch(%q) error = %v", tc.src, err)
			}
			if got != tc.want {
				t.Fatalf("translateBranch(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestTranslateBranchRejections(t *testing.T) {
	tests := []struct{ src, want string }{
		{`\w+`, `\w has different`},
		{`\W`, `\W has different`},
		{`\bword\b`, `\b has different`},
		{`^start`, "anchor"},
		{`end$`, "anchor"},
		{`[\S]`, "set subtraction"},
		{`[\D]`, "set subtraction"},
		{`[abc`, "unterminated character class"},
		{`abc\`, "trailing backslash"},
		// Set operations: the reference intersects/subtracts, Go takes the
		// operator characters as literal class members. Both compile.
		{`[\p{L}&&\p{Greek}]`, "set operation"},
		{`[a-z--[aeiou]]`, "set operation"},
		{`[a~~b]`, "set operation"},
		{`[[a-z]&&[b]]`, "nested character class"},
		{`[a[b]]`, "nested character class"},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			_, err := translateBranch(tc.src)
			if err == nil {
				t.Fatalf("translateBranch(%q) error = nil, want %q", tc.src, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("translateBranch(%q) error = %q, want it to contain %q", tc.src, err, tc.want)
			}
		})
	}
}

// matchSpacesNotBeforeNonSpace is the one branch Go's regexp cannot express.
// It must give a rune back unless the whitespace run ends the input.
func TestMatchSpacesNotBeforeNonSpace(t *testing.T) {
	tests := []struct {
		s    string
		pos  int
		end  int
		want bool
	}{
		{"  spaced", 0, 1, true},
		{"   spaced", 0, 2, true},
		{" spaced", 0, 0, false},
		{"spaced", 0, 0, false},
		{"a  ", 1, 3, true},
		{"a ", 1, 2, true},
		{"  ", 0, 2, true},
		{"  x", 0, 2, true},
		{"　x", 0, 0, false},
	}
	for _, tc := range tests {
		end, ok := matchSpacesNotBeforeNonSpace(tc.s, tc.pos)
		if ok != tc.want || (ok && end != tc.end) {
			t.Errorf("matchSpacesNotBeforeNonSpace(%q, %d) = %d, %v; want %d, %v",
				tc.s, tc.pos, end, ok, tc.end, tc.want)
		}
	}
}

// The alternation must behave leftmost-first: the earliest offset wins, and at
// that offset the earliest branch wins even when a later one matches more.
func TestAltMatcherIsLeftmostFirst(t *testing.T) {
	m, err := newAltMatcher(`ab|abc|b`)
	if err != nil {
		t.Fatalf("newAltMatcher: %v", err)
	}
	start, end, ok := m.find("xabcx", 0)
	if !ok || start != 1 || end != 3 {
		t.Fatalf("find = %d, %d, %v; want 1, 3, true (ab before abc)", start, end, ok)
	}
}

func TestAltMatcherOnTheAudio8Pattern(t *testing.T) {
	m, err := newAltMatcher(audio8SplitPattern)
	if err != nil {
		t.Fatalf("newAltMatcher: %v", err)
	}
	tests := []struct {
		in   string
		want []string
	}{
		{"Hello world", []string{"Hello", " world"}},
		{"  spaced  ", []string{" ", " spaced", "  "}},
		{"x  \n y", []string{"x", "  \n", " y"}},
		{"foo\n\nbar", []string{"foo", "\n\n", "bar"}},
		{"a1b23c", []string{"a", "1", "b", "2", "3", "c"}},
		{"a b", []string{"a", " b"}},
		{"tail//", []string{"tail", "//"}},
	}
	for _, tc := range tests {
		got := splitString(tc.in, m, behaviorIsolated, false)
		if !slices.Equal(got, tc.want) {
			t.Errorf("split(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPunctuationSplitIsContiguous(t *testing.T) {
	m := runeMatcher{ok: isPunctuation}
	tests := []struct {
		in   string
		want []string
	}{
		{"a,,b!c", []string{"a", ",,", "b", "!", "c"}},
		{",,,", []string{",,,"}},
		{"a", []string{"a"}},
		{"", nil},
		{"don't", []string{"don", "'", "t"}},
	}
	for _, tc := range tests {
		got := splitString(tc.in, m, behaviorContiguous, false)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("split(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// invert is honoured but cannot change the pieces under Isolated or
// Contiguous; splitString's comment says so, and this holds it to it.
func TestInvertDoesNotChangeTheImplementedBehaviors(t *testing.T) {
	digits := runeMatcher{ok: func(r rune) bool { return r >= '0' && r <= '9' }}
	for _, in := range []string{"", "a1b", "aa11bb", "123", "abc", "1a", "a1"} {
		for _, b := range []splitBehavior{behaviorIsolated, behaviorContiguous} {
			plain := splitString(in, digits, b, false)
			inverted := splitString(in, digits, b, true)
			if !slices.Equal(plain, inverted) {
				t.Errorf("split(%q, behavior %d): invert changed %q into %q", in, b, plain, inverted)
			}
		}
	}
}

func TestLiteralSplit(t *testing.T) {
	got := splitString("a-b-c", literalMatcher{lit: "-"}, behaviorIsolated, false)
	if want := []string{"a", "-", "b", "-", "c"}; !slices.Equal(got, want) {
		t.Fatalf("split = %q, want %q", got, want)
	}
}
