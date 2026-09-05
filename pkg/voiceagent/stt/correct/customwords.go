package correct

import (
	"context"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultThreshold is the fuzzy acceptance score; lower is stricter.
	DefaultThreshold = 0.12
	// CandidateBand is the score below which a rejected fuzzy match is still
	// reported as a candidate for a later stage.
	CandidateBand = 0.45

	// soundexCap bounds the raw edit ratio at which a Soundex match may boost
	// the score; without it "worked" reaches "workitem". A third of the
	// letters is where two edits on a six-letter name land ("alama" for
	// Ollama), and where "cloud" would reach Claude if the stoplist did not
	// hold it.
	soundexCap   = 0.34
	soundexBoost = 0.3
	maxSpan      = 3
)

// Replacement records one change the matcher made.
type Replacement struct {
	Span  string  // original span, without surrounding punctuation
	Term  string  // canonical text written in its place
	Score float64 // 0 for exact and alias matches
	Exact bool
}

// Candidate is a span that scored inside the candidate band against a term
// without reaching the threshold. It is reported for later stages, never applied.
type Candidate struct {
	Span  string
	Term  string
	Score float64
}

type term struct {
	text string
	key  string
	sdx  string
}

// CustomWords restores canonical spellings of names and jargon. Every term
// and alias matches exactly on its key; eligible terms also match fuzzily
// under the guards that keep real words untouched.
type CustomWords struct {
	threshold float64
	exact     map[string]*term
	fuzzy     []*term
}

// NewCustomWords builds the stage. A threshold of zero or less selects
// DefaultThreshold.
func NewCustomWords(terms []Term, threshold float64) *CustomWords {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	c := &CustomWords{threshold: threshold, exact: make(map[string]*term, 2*len(terms))}
	for _, in := range terms {
		k := Key(in.Text)
		if k == "" {
			continue
		}
		t := &term{text: in.Text, key: k, sdx: Soundex(k)}
		c.addExact(k, t)
		for _, a := range in.Aliases {
			c.addExact(Key(a), t)
		}
		if in.Fuzzy && FuzzyEligible(in.Text) {
			c.fuzzy = append(c.fuzzy, t)
		}
	}
	return c
}

func (c *CustomWords) addExact(key string, t *term) {
	if key == "" {
		return
	}
	if _, dup := c.exact[key]; !dup {
		c.exact[key] = t
	}
}

// Name implements Stage.
func (c *CustomWords) Name() string { return "custom_words" }

// Empty reports whether the stage has nothing to match.
func (c *CustomWords) Empty() bool { return c == nil || len(c.exact) == 0 }

// Apply implements Stage.
func (c *CustomWords) Apply(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return text, err
	}
	out, _, _ := c.Scan(text)
	return out, nil
}

// Candidates reports the spans that fell in the candidate band.
func (c *CustomWords) Candidates(text string) []Candidate {
	_, _, cands := c.Scan(text)
	return cands
}

// Scan returns the corrected text with every replacement made and every
// candidate seen. Whitespace and punctuation outside the replaced spans are
// preserved byte for byte.
func (c *CustomWords) Scan(text string) (string, []Replacement, []Candidate) {
	if c.Empty() || text == "" {
		return text, nil, nil
	}
	toks := tokenize(text)
	st := newScanState(toks)
	var (
		out   strings.Builder
		reps  []Replacement
		cands []Candidate
		last  int
	)
	emit := func(i, n int, t *term, score float64, exact bool) {
		lead, cur, trail := splitCore(text, toks[i], toks[i+n-1])
		repl := coerceCase(t.text, cur)
		if repl == cur {
			return
		}
		out.WriteString(text[last:toks[i].start])
		out.WriteString(lead)
		out.WriteString(repl)
		out.WriteString(trail)
		last = toks[i+n-1].end
		reps = append(reps, Replacement{Span: cur, Term: t.text, Score: score, Exact: exact})
	}
	for i := 0; i < len(toks); {
		if t, n := c.exactAt(st, i); t != nil {
			emit(i, n, t, 0, true)
			i += n
			continue
		}
		m := c.fuzzyAt(st, i)
		if m.term == nil {
			i++
			continue
		}
		if m.score < c.threshold {
			emit(i, m.n, m.term, m.score, false)
		} else {
			_, cur, _ := splitCore(text, toks[i], toks[i+m.n-1])
			cands = append(cands, Candidate{Span: cur, Term: m.term.text, Score: m.score})
		}
		i += m.n
	}
	if len(reps) == 0 {
		return text, nil, cands
	}
	out.WriteString(text[last:])
	return out.String(), reps, cands
}

// exactAt returns the longest exact match starting at token i.
func (c *CustomWords) exactAt(st *scanState, i int) (*term, int) {
	for n := min(maxSpan, len(st.toks)-i); n >= 1; n-- {
		k := st.key(i, n)
		if k == "" {
			continue
		}
		if t := c.exact[k]; t != nil {
			return t, n
		}
	}
	return nil, 0
}

type match struct {
	term  *term
	n     int
	score float64
}

// fuzzyAt returns the best fuzzy match starting at token i: the lowest score,
// and among equal scores the shorter span.
func (c *CustomWords) fuzzyAt(st *scanState, i int) match {
	var best match
	toks := st.toks
	for n := 1; n <= maxSpan && i+n <= len(toks); n++ {
		k := st.key(i, n)
		if k == "" || !spanFuzzyOK(toks, i, n) || nearProperNoun(toks, i, n) {
			continue
		}
		for _, t := range c.fuzzy {
			sc, ok := st.score(i, n, t)
			if !ok || sc >= CandidateBand {
				continue
			}
			if n > 1 && !st.beatsSubspans(i, n, t, sc) {
				continue
			}
			if best.term == nil || sc < best.score || (sc == best.score && n < best.n) {
				best = match{term: t, n: n, score: sc}
			}
		}
	}
	return best
}

// scanState holds what one Scan computes once per span rather than once per
// term: span keys, their Soundex codes, and the edit-distance rows.
type scanState struct {
	toks    []token
	keys    []string // keys[i*maxSpan+n-1]; "" when the span is not usable
	sdx     []string
	sdxDone []bool
	prev    []int
	cur     []int
}

func newScanState(toks []token) *scanState {
	st := &scanState{
		toks:    toks,
		keys:    make([]string, len(toks)*maxSpan),
		sdx:     make([]string, len(toks)*maxSpan),
		sdxDone: make([]bool, len(toks)*maxSpan),
	}
	for i := range toks {
		for n := 1; n <= maxSpan && i+n <= len(toks); n++ {
			if spanOK(toks, i, n) {
				st.keys[i*maxSpan+n-1] = spanKey(toks, i, n)
			}
		}
	}
	return st
}

func (st *scanState) key(i, n int) string { return st.keys[i*maxSpan+n-1] }

func (st *scanState) soundex(i, n int) string {
	idx := i*maxSpan + n - 1
	if !st.sdxDone[idx] {
		st.sdx[idx] = Soundex(st.keys[idx])
		st.sdxDone[idx] = true
	}
	return st.sdx[idx]
}

// beatsSubspans is the edge guard: a multi-token span is accepted only when it
// scores strictly better than the same span without its first or last token.
func (st *scanState) beatsSubspans(i, n int, t *term, sc float64) bool {
	if l, ok := st.score(i+1, n-1, t); ok && l <= sc {
		return false
	}
	if r, ok := st.score(i, n-1, t); ok && r <= sc {
		return false
	}
	return true
}

// score is the normalized edit distance between a span key and a term, with
// the Soundex boost applied only when the raw ratio is already close. The
// second value is false when the lengths differ too much to compare.
func (st *scanState) score(i, n int, t *term) (float64, bool) {
	k := st.key(i, n)
	d := len(k) - len(t.key)
	if d < 0 {
		d = -d
	}
	if float64(d) > math.Max(0.25*float64(len(t.key)), 2) {
		return 0, false
	}
	raw := float64(st.lev(k, t.key)) / float64(max(len(k), len(t.key)))
	if raw <= soundexCap && t.sdx != "" && st.soundex(i, n) == t.sdx {
		return raw * soundexBoost, true
	}
	return raw, true
}

// lev is Levenshtein over the state's reusable rows.
func (st *scanState) lev(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	need := len(b) + 1
	if cap(st.prev) < need {
		st.prev = make([]int, need)
		st.cur = make([]int, need)
	}
	prev, cur := st.prev[:need], st.cur[:need]
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	st.prev, st.cur = prev, cur
	return prev[len(b)]
}

type token struct {
	start, end int
	key        string
	boundary   bool // ends a clause: no span continues past it
	ascii      bool
	capital    bool // first letter is upper case
}

func tokenize(text string) []token {
	var toks []token
	start := -1
	for i, r := range text {
		if unicode.IsSpace(r) {
			if start >= 0 {
				toks = append(toks, newToken(text, start, i))
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		toks = append(toks, newToken(text, start, len(text)))
	}
	return toks
}

func newToken(text string, start, end int) token {
	raw := text[start:end]
	t := token{start: start, end: end, key: Key(raw), ascii: true}
	last, _ := utf8.DecodeLastRuneInString(raw)
	t.boundary = strings.ContainsRune(".!?,;:", last)
	for _, r := range raw {
		if unicode.IsLetter(r) {
			t.capital = unicode.IsUpper(r)
			break
		}
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] >= utf8.RuneSelf {
			t.ascii = false
			break
		}
	}
	return t
}

// spanOK: every token has a key and none but the last ends a clause.
func spanOK(toks []token, i, n int) bool {
	for k := i; k < i+n; k++ {
		if toks[k].key == "" {
			return false
		}
		if k < i+n-1 && toks[k].boundary {
			return false
		}
	}
	return true
}

// spanFuzzyOK: ASCII throughout and at least one token that is neither short
// nor a common word.
func spanFuzzyOK(toks []token, i, n int) bool {
	allStop := true
	for k := i; k < i+n; k++ {
		if !toks[k].ascii {
			return false
		}
		if !isStopKey(toks[k].key) {
			allStop = false
		}
	}
	return !allStop
}

// nearProperNoun reports whether the span sits beside a capitalized rare word
// in the same clause. "Harisuddin Samanathan" is somebody's name, not a
// misheard Samantha, and renaming a person is the worst mistake this stage
// can make.
func nearProperNoun(toks []token, i, n int) bool {
	if i > 0 && !toks[i-1].boundary && looksProperNoun(toks[i-1]) {
		return true
	}
	if j := i + n; j < len(toks) && !toks[j-1].boundary && looksProperNoun(toks[j]) {
		return true
	}
	return false
}

func looksProperNoun(t token) bool {
	return t.capital && !isStopKey(t.key)
}

func spanKey(toks []token, i, n int) string {
	if n == 1 {
		return toks[i].key
	}
	var b strings.Builder
	for k := i; k < i+n; k++ {
		b.WriteString(toks[k].key)
	}
	return b.String()
}

// splitCore splits the span from first through last into the punctuation
// before its first letter or digit, the core, and the punctuation after its last.
func splitCore(text string, first, last token) (lead, core, trail string) {
	s, e := first.start, last.end
	cs := s
	for cs < e {
		r, size := utf8.DecodeRuneInString(text[cs:e])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		cs += size
	}
	ce := e
	for ce > cs {
		r, size := utf8.DecodeLastRuneInString(text[cs:ce])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		ce -= size
	}
	return text[s:cs], text[cs:ce], text[ce:e]
}

// coerceCase writes term in the case pattern of the span it replaces: an
// all-caps span stays all caps, a term with its own capitals keeps them, and a
// capitalized span capitalizes a lower-case term.
func coerceCase(term, span string) string {
	letters, upper := 0, 0
	firstUpper := false
	for _, r := range span {
		if !unicode.IsLetter(r) {
			continue
		}
		if letters == 0 {
			firstUpper = unicode.IsUpper(r)
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	switch {
	case letters >= 2 && upper == letters:
		return strings.ToUpper(term)
	case strings.ToLower(term) != term:
		return term
	case firstUpper:
		r, size := utf8.DecodeRuneInString(term)
		return string(unicode.ToUpper(r)) + term[size:]
	}
	return term
}
