package summon

import (
	"strings"
	"unicode"
)

// Lookup validates a semantic addressee against the same names and collision
// rules used by the roster. A model cannot select an arbitrary storage ID.
func (r *Roster) Lookup(name string) (Address, error) {
	entry, ok := r.byName[normalizeName(name)]
	if !ok {
		return Address{}, &UnknownTargetError{Target: name}
	}
	return r.address(entry, "", "", "conversation-address", false)
}

// Mentions is only a cheap candidate check, never an addressing decision.
// Every name position and punctuation style reaches semantic classification.
func (r *Roster) Mentions(text string) bool {
	haystack := mentionWords(text)
	for _, name := range r.names {
		if strings.Contains(haystack, mentionWords(name.value)) {
			return true
		}
	}
	return false
}

// mentionWords is text as space-delimited lowercase words, so a name compares
// whole words regardless of punctuation.
func mentionWords(s string) string {
	return " " + strings.Join(strings.FieldsFunc(strings.ToLower(s), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }), " ") + " "
}
