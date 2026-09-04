// Package summon decides whether a finalized transcript addresses the agent by
// name. It is the STT-side half of name summon (design D5 / R2): an exact-match
// table over the persona's display name and its aliases, never a model. The
// cost of that choice is honest — the decision lands only after the sentence is
// over, and a wake-word model is the follow-up, not this.
package summon

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fillers are the throwaway words people put in front of a name. Exported so
// the docs, the tests and the matcher all quote one list.
var Fillers = []string{"hey", "ok", "okay", "hi", "hello", "yo"}

// nameBoundary is the punctuation that may close a name. Anything else glued to
// it is a different word: "obeying the rules" is not an address.
const nameBoundary = ",.!?:;"

// Matcher answers whether an utterance opens with one of the agent's names.
// The zero value never matches, which is the safe direction: a persona with no
// display name must not turn every utterance in the room into a summon.
type Matcher struct {
	// names is normalized and sorted longest first, so "uncle fu" is tried
	// before the "uncle" that is also derived from it.
	names []string
}

// NewMatcher builds the table for agentName plus aliases. A multi-word display
// name also answers to its first word (R2): people address "Uncle Fu" as
// "Uncle". Names are lowercased, internal whitespace collapsed, and duplicates
// dropped.
func NewMatcher(agentName string, aliases []string) Matcher {
	candidates := make([]string, 0, len(aliases)+2)
	candidates = append(candidates, agentName)
	if words := strings.Fields(agentName); len(words) > 1 {
		candidates = append(candidates, words[0])
	}
	candidates = append(candidates, aliases...)

	var names []string
	for _, candidate := range candidates {
		name := strings.Join(strings.Fields(strings.ToLower(candidate)), " ")
		if name == "" || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), cmp.Compare(a, b))
	})
	return Matcher{names: names}
}

// Names reports the wake words in match order (longest first), for the activity
// panel and the tests.
func (m Matcher) Names() []string {
	return slices.Clone(m.names)
}

// Addressed reports whether text opens with one of the agent's names, allowing
// a single leading filler. Case-insensitive; leading punctuation and quotes are
// ignored so a transcriber's opening quote cannot hide the name.
func (m Matcher) Addressed(text string) bool {
	if len(m.names) == 0 {
		return false
	}
	body := trimLead(strings.ToLower(text))
	if body == "" {
		return false
	}
	if m.startsWithName(body) {
		return true
	}
	// Exactly one filler, and nothing else, may stand in front of the name:
	// "hey obey" addresses the agent, "hey there obey" is talk about it.
	rest, ok := cutFiller(body)
	return ok && m.startsWithName(rest)
}

func (m Matcher) startsWithName(body string) bool {
	for _, name := range m.names {
		rest, ok := strings.CutPrefix(body, name)
		if !ok {
			continue
		}
		if rest == "" || closesName(rest) {
			return true
		}
	}
	return false
}

// cutFiller removes one leading filler and returns what follows. The filler
// must be a word of its own — "hidden" does not begin with the filler "hi".
func cutFiller(body string) (string, bool) {
	for _, filler := range Fillers {
		rest, ok := strings.CutPrefix(body, filler)
		if !ok || rest == "" || !closesName(rest) {
			continue
		}
		return trimLead(rest), true
	}
	return "", false
}

// closesName reports whether rest starts with something that ends the word
// before it.
func closesName(rest string) bool {
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsSpace(r) || strings.ContainsRune(nameBoundary, r)
}

// trimLead drops leading space, punctuation and symbols — quotes, dashes, the
// comma after a filler — so the name can be compared against the first word.
func trimLead(s string) string {
	return strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}
