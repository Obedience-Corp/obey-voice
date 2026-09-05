package correct

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxTerms caps the merged term list; the matcher is linear in it.
	MaxTerms = 128
	// minFuzzyKeyLen is the shortest key a term may have to be fuzzy-matched.
	minFuzzyKeyLen = 5
	// minStopKeyLen is the shortest token that can take part in a fuzzy span.
	minStopKeyLen = 3
)

// Term is a canonical spelling the matcher restores in transcripts.
type Term struct {
	// Text is the replacement written into the transcript.
	Text string
	// Aliases are alternative spellings matched exactly, never fuzzily.
	Aliases []string
	// Fuzzy opts the term into fuzzy matching. It is honored only when
	// FuzzyEligible(Text) holds.
	Fuzzy bool
}

// Key returns the term's match key.
func (t Term) Key() string { return Key(t.Text) }

// ParseTerm parses one config entry:
//
//	Text                 exact matches only
//	Text!                also fuzzy, when eligible
//	Text=alias1,alias2   aliases are exact-only alternative spellings
//	Text!=alias1         both
func ParseTerm(entry string) (Term, error) {
	s := strings.TrimSpace(entry)
	if s == "" {
		return Term{}, errors.New("empty term")
	}
	var t Term
	if i := strings.Index(s, "="); i >= 0 {
		for _, a := range strings.Split(s[i+1:], ",") {
			if a = strings.TrimSpace(a); a != "" && Key(a) != "" {
				t.Aliases = append(t.Aliases, a)
			}
		}
		s = strings.TrimSpace(s[:i])
	}
	if strings.HasSuffix(s, "!") {
		t.Fuzzy = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "!"))
	}
	if Key(s) == "" {
		return Term{}, fmt.Errorf("term %q has no letters or digits", entry)
	}
	t.Text = s
	return t, nil
}

// ParseTerms parses config entries, stopping at the first invalid one.
func ParseTerms(entries []string) ([]Term, error) {
	terms := make([]Term, 0, len(entries))
	for _, e := range entries {
		t, err := ParseTerm(e)
		if err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	return terms, nil
}

// FuzzyEligible reports whether a term may be fuzzy-matched: its key has at
// least five characters and it is not a common word or name.
func FuzzyEligible(text string) bool {
	k := Key(text)
	return len(k) >= minFuzzyKeyLen && !IsCommon(k)
}

// PersonaTerms turns persona display names into terms. A name is fuzzy only
// when FuzzyEligible holds, so "Santa" and "Joker" match exactly while
// "Samantha" and "Veronica" also catch misspellings.
func PersonaTerms(names []string) []Term {
	terms := make([]Term, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if Key(n) == "" {
			continue
		}
		terms = append(terms, Term{Text: n, Fuzzy: FuzzyEligible(n)})
	}
	return terms
}

// Merge combines explicit terms with derived ones. Explicit entries win when
// keys collide, order is preserved, and the result is capped at MaxTerms; the
// second value counts the terms dropped by the cap.
func Merge(explicit, derived []Term) ([]Term, int) {
	seen := make(map[string]struct{}, len(explicit)+len(derived))
	out := make([]Term, 0, len(explicit)+len(derived))
	dropped := 0
	add := func(t Term) {
		k := t.Key()
		if k == "" {
			return
		}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		if len(out) >= MaxTerms {
			dropped++
			return
		}
		out = append(out, t)
	}
	for _, t := range explicit {
		add(t)
	}
	for _, t := range derived {
		add(t)
	}
	return out, dropped
}
