package hfbpe

// addedVocab matches the tokenizer's added tokens against raw input before any
// normalization or pre-tokenization, exactly as tokenizers' AddedVocabulary
// does: leftmost-longest, non-overlapping, over bytes. A hit becomes its own id
// and never reaches the BPE.
type addedVocab struct {
	byContent map[string]int64
	// maxLenFor[b] is the longest added token starting with byte b (0 = none),
	// which lets the scan skip most offsets with a single lookup.
	maxLenFor [256]int
}

func newAddedVocab(tokens map[string]int64) *addedVocab {
	a := &addedVocab{byContent: tokens}
	for content := range tokens {
		if content == "" {
			continue
		}
		if n := len(content); n > a.maxLenFor[content[0]] {
			a.maxLenFor[content[0]] = n
		}
	}
	return a
}

// find returns the leftmost-longest added token at or after from.
func (a *addedVocab) find(s string, from int) (start, end int, id int64, ok bool) {
	for i := from; i < len(s); i++ {
		max := a.maxLenFor[s[i]]
		if max == 0 {
			continue
		}
		if max > len(s)-i {
			max = len(s) - i
		}
		for n := max; n > 0; n-- {
			if id, hit := a.byContent[s[i:i+n]]; hit {
				return i, i + n, id, true
			}
		}
	}
	return 0, 0, 0, false
}
