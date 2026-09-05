package correct

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// fillerKey matches the hesitation sounds a transcriber writes down: um, uh,
// hmm and their stretched spellings. "oh", "ah" and "like" are left alone; they
// carry meaning as often as not, and "uh-huh" / "mm-hmm" are answers.
var fillerKey = regexp.MustCompile(`^(u+m+|u+h+|uhm|h+m+|m+m+|erm|ehm|ehh)$`)

// conjunctionKeys are the words whose trailing comma belonged to the filler
// that followed them: "and, uh, we'll" reads "and we'll", not "and, we'll".
var conjunctionKeys = map[string]bool{"and": true, "but": true, "or": true}

// Fillers drops hesitation sounds and collapses a word repeated three or more
// times in a row. It never empties a transcript: a lone "Hmm." is an answer.
type Fillers struct{}

// Name implements Stage.
func (Fillers) Name() string { return "fillers" }

// Apply implements Stage.
func (Fillers) Apply(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return text, err
	}
	return RemoveFillers(text), nil
}

// RemoveFillers is Fillers.Apply without the context.
func RemoveFillers(text string) string {
	out, _ := ScanFillers(text)
	return out
}

// ScanFillers removes fillers and reports the words it dropped.
func ScanFillers(text string) (string, []string) {
	toks := tokenize(text)
	if len(toks) == 0 {
		return text, nil
	}
	drop := make([]bool, len(toks))
	changed := false
	for i := range toks {
		if fillerKey.MatchString(toks[i].key) && !toks[i].hyphenated(text) {
			drop[i] = true
			changed = true
		}
	}
	// A word stuttered three or more times in a row keeps one copy. Only bare
	// repeats count: "Well, well, well" and "No, no, no" are said on purpose.
	runStart := 0
	for i := 1; i <= len(toks); i++ {
		if i < len(toks) && toks[i].key != "" && !drop[i] && toks[i].key == toks[runStart].key && bareWord(text, toks[i-1]) {
			continue
		}
		if i-runStart >= 3 {
			for k := runStart + 1; k < i; k++ {
				drop[k] = true
			}
			changed = true
		}
		runStart = i
	}
	if !changed {
		return text, nil
	}
	out := rebuildWithoutTokens(text, toks, drop)
	if strings.TrimSpace(out) == "" || !hasLetter(out) {
		return text, nil
	}
	var dropped []string
	for i, t := range toks {
		if drop[i] {
			_, core, _ := splitCore(text, t, t)
			dropped = append(dropped, core)
		}
	}
	return out, dropped
}

// hyphenated reports whether the token is joined to another word, as in
// "uh-huh" or "mm-hmm"; those are answers, not hesitation.
func (t token) hyphenated(text string) bool {
	return strings.ContainsRune(text[t.start:t.end], '-')
}

// rebuildWithoutTokens writes text without the dropped tokens, tidying the
// punctuation and spacing each removal leaves behind.
func rebuildWithoutTokens(text string, toks []token, drop []bool) string {
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	capitalizeNext := false
	prevKeptIdx := -1
	for i, t := range toks {
		if !drop[i] {
			word := text[t.start:t.end]
			if capitalizeNext {
				word = capitalizeFirst(word)
				capitalizeNext = false
			}
			b.WriteString(text[last:t.start])
			b.WriteString(word)
			last = t.end
			prevKeptIdx = i
			continue
		}
		lead, _, trail := splitCore(text, t, t)
		prevKept := i > 0 && !drop[i-1]
		prevRaw := ""
		if prevKept {
			prevRaw = text[toks[i-1].start:toks[i-1].end]
		}
		// "and, uh, we'll": the comma after "and" was the filler's.
		if prevKept && conjunctionKeys[toks[i-1].key] && strings.HasSuffix(prevRaw, ",") && strings.HasPrefix(trail, ",") {
			trimmed := strings.TrimSuffix(b.String(), ",")
			b.Reset()
			b.WriteString(trimmed)
		}
		if prevKeptIdx < 0 || endsSentence(text, toks[prevKeptIdx]) {
			capitalizeNext = true
		}
		sep := text[last:t.start]
		last = t.end
		// Sentence punctuation on a mid-sentence filler closes the sentence
		// it interrupted: "I think um. Then" reads "I think. Then".
		if prevKept && !endsSentence(text, toks[i-1]) && strings.ContainsAny(trail, ".!?") {
			trimmed := strings.TrimRight(b.String(), " \t")
			b.Reset()
			b.WriteString(trimmed)
			b.WriteString(strings.TrimLeft(trail, ",;: "))
			capitalizeNext = true
			continue
		}
		b.WriteString(sep)
		b.WriteString(lead)
		// Swallow the spaces after the dropped word, but not a line break.
		for last < len(text) {
			r, size := utf8.DecodeRuneInString(text[last:])
			if r == '\n' || !unicode.IsSpace(r) {
				break
			}
			last += size
		}
		if last == len(text) || text[last] == '\n' {
			trimmed := strings.TrimRight(b.String(), " \t")
			b.Reset()
			b.WriteString(trimmed)
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

// bareWord reports whether a token carries no punctuation at all.
func bareWord(text string, t token) bool {
	lead, _, trail := splitCore(text, t, t)
	return lead == "" && trail == ""
}

func endsSentence(text string, t token) bool {
	r, _ := utf8.DecodeLastRuneInString(text[t.start:t.end])
	return r == '.' || r == '!' || r == '?'
}

func capitalizeFirst(word string) string {
	for i, r := range word {
		if unicode.IsLetter(r) {
			if unicode.IsUpper(r) {
				return word
			}
			return word[:i] + string(unicode.ToUpper(r)) + word[i+utf8.RuneLen(r):]
		}
	}
	return word
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
