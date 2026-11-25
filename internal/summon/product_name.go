package summon

import (
	"regexp"
	"strings"
)

// ProductName is the application's name. A persona name heard only inside it,
// as "Obey" in "a test of Obey Voice", is talk about the app, not an address.
const ProductName = "Obey Voice"

// productNamePattern also accepts the transcriber spellings "Obey-Voice",
// "Obey - Voice", "Obey—Voice", "Obey_Voice" and "ObeyVoice". A comma or
// period never joins the words.
var productNamePattern = func() *regexp.Regexp {
	words := strings.Fields(ProductName)
	for i, word := range words {
		words[i] = regexp.QuoteMeta(word)
	}
	return regexp.MustCompile(`(?i)\b` + strings.Join(words, `(?:\s*[-_‐‑–—]\s*|\s*)`) + `\b`)
}()

// productNameCovers reports whether text opens with the product name and the
// opening name, consumed bytes long, is only part of it.
func productNameCovers(text string, consumed int) bool {
	loc := productNamePattern.FindStringIndex(text)
	return loc != nil && loc[0] == 0 && loc[1] > consumed
}

// NamedOnlyInProductName reports whether the persona's names occur in text
// only inside the product name. A name that contains the product name, such
// as one that is the product name or goes on past it, is still its own name.
func (r *Roster) NamedOnlyInProductName(text, personaID string) bool {
	if r == nil {
		return false
	}
	if !productNamePattern.MatchString(text) {
		return false
	}
	var names []string
	for _, entry := range r.names {
		if entry.personaID == personaID {
			names = append(names, mentionWords(entry.value))
		}
	}
	mentioned := func(s string) bool {
		haystack := mentionWords(s)
		for _, name := range names {
			if strings.Contains(haystack, name) {
				return true
			}
		}
		return false
	}
	canonical := productNamePattern.ReplaceAllLiteralString(text, ProductName)
	if !mentioned(canonical) {
		return false
	}
	product, haystack := mentionWords(ProductName), mentionWords(canonical)
	for _, name := range names {
		if strings.Contains(name, product) && strings.Contains(haystack, name) {
			return false
		}
	}
	for _, segment := range productNamePattern.Split(text, -1) {
		if mentioned(segment) {
			return false
		}
	}
	return true
}
