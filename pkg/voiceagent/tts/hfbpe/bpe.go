package hfbpe

type mergePair struct{ a, b uint32 }

type mergeRule struct {
	rank int32
	id   uint32
}

// merge applies the BPE merge table to one pre-token.
//
// Each round takes the lowest-ranked pair and, among equal ranks, the leftmost
// occurrence — and merges that one occurrence only. That is what tokenizers'
// Word::merge_all does with its rank/position-ordered heap, and it is not the
// same as the classic "merge every occurrence of the best pair" loop: a merge
// can create a pair that outranks the one being applied, and the two orders
// then disagree. Pre-tokens are a handful of symbols, so the O(n^2) scan costs
// nothing worth trading correctness for.
func (t *Tokenizer) merge(sym []uint32) []uint32 {
	for len(sym) > 1 {
		best := int32(-1)
		pos := -1
		var id uint32
		for i := 0; i+1 < len(sym); i++ {
			rule, ok := t.merges[mergePair{sym[i], sym[i+1]}]
			if !ok || (pos >= 0 && rule.rank >= best) {
				continue
			}
			best, pos, id = rule.rank, i, rule.id
		}
		if pos < 0 {
			return sym
		}
		sym[pos] = id
		sym = append(sym[:pos+1], sym[pos+2:]...)
	}
	return sym
}
