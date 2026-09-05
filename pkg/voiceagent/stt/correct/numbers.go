package correct

import (
	"context"
	"strconv"
	"strings"
)

// Numbers writes spoken numbers as digits where a reader would: compounds
// ("thirty five" → 35), values of ten and above, percentages and currency
// at any size ("five percent" → 5%, "twenty thousand dollars" → $20,000),
// decimals, ordinals from 10th, and years ("twenty twenty two" → 2022).
// Small counts stay words ("two years", "one thing"), a run that does not
// read as one number is left alone ("one seventy", "two three four"), and
// vague quantities are never digits ("a few hundred").
type Numbers struct{}

// Name implements Stage.
func (Numbers) Name() string { return "numbers" }

// Apply implements Stage.
func (Numbers) Apply(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return text, err
	}
	return ConvertNumbers(text), nil
}

const (
	wordUnit    = iota + 1 // zero..nineteen
	wordTens               // twenty..ninety
	wordScale              // hundred, thousand, million, billion
	wordOrdinal            // first.. (value in val)
	wordAnd
	wordPoint
	wordOh
	wordArticle // "a", only before a scale word
)

type numWord struct {
	kind int
	val  int
	raw  string // the scale word, kept for "6 million"
}

var unitWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17,
	"eighteen": 18, "nineteen": 19,
}

var tensWords = map[string]int{
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60,
	"seventy": 70, "eighty": 80, "ninety": 90,
}

var scaleWords = map[string]int{"hundred": 100, "thousand": 1000, "million": 1000000, "billion": 1000000000}

// ordinalWords omit "second": "a thirty second delay" is a duration.
var ordinalWords = map[string]int{
	"first": 1, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7,
	"eighth": 8, "ninth": 9, "tenth": 10, "eleventh": 11, "twelfth": 12,
	"thirteenth": 13, "fourteenth": 14, "fifteenth": 15, "sixteenth": 16,
	"seventeenth": 17, "eighteenth": 18, "nineteenth": 19, "twentieth": 20,
	"thirtieth": 30, "fortieth": 40, "fiftieth": 50, "sixtieth": 60,
	"seventieth": 70, "eightieth": 80, "ninetieth": 90, "hundredth": 100,
	"thousandth": 1000,
}

var unitSuffix = map[string]string{
	"percent": "%", "percentage": "%",
	"dollar": "$", "dollars": "$", "bucks": "$",
}

func classifyWord(key string) (numWord, bool) {
	if v, ok := unitWords[key]; ok {
		return numWord{kind: wordUnit, val: v}, true
	}
	if v, ok := tensWords[key]; ok {
		return numWord{kind: wordTens, val: v}, true
	}
	if v, ok := scaleWords[key]; ok {
		return numWord{kind: wordScale, val: v, raw: key}, true
	}
	if v, ok := ordinalWords[key]; ok {
		return numWord{kind: wordOrdinal, val: v}, true
	}
	switch key {
	case "and":
		return numWord{kind: wordAnd}, true
	case "point":
		return numWord{kind: wordPoint}, true
	case "oh":
		return numWord{kind: wordOh}, true
	case "a":
		return numWord{kind: wordArticle}, true
	}
	return numWord{}, false
}

// tokenWords splits a token into its number words: "twenty-five" is two.
// The second value is false when any part is not a number word.
func tokenWords(text string, t token) ([]numWord, bool) {
	_, core, _ := splitCore(text, t, t)
	parts := strings.Split(core, "-")
	words := make([]numWord, 0, len(parts))
	for _, p := range parts {
		w, ok := classifyWord(Key(p))
		if !ok {
			return nil, false
		}
		words = append(words, w)
	}
	return words, true
}

func isConnector(w numWord) bool {
	return w.kind == wordAnd || w.kind == wordPoint || w.kind == wordOh || w.kind == wordArticle
}

// ConvertNumbers is Numbers.Apply without the context.
func ConvertNumbers(text string) string {
	out, _ := ScanNumbers(text)
	return out
}

// ScanNumbers converts spoken numbers and reports each conversion as a
// Replacement whose Term is the digits written.
func ScanNumbers(text string) (string, []Replacement) {
	toks := tokenize(text)
	if len(toks) == 0 {
		return text, nil
	}
	var b strings.Builder
	var reps []Replacement
	last := 0
	changed := false
	for i := 0; i < len(toks); {
		n, words := numberRun(text, toks, i)
		if n == 0 {
			i++
			continue
		}
		unit := ""
		consumed := n
		if i+n < len(toks) && !toks[i+n-1].boundary {
			if u, ok := unitSuffix[toks[i+n].key]; ok {
				unit = u
				consumed++
			}
		}
		out, ok := renderNumber(words, unit)
		if !ok {
			i += n
			continue
		}
		// "five to ten minutes" reads "5 to 10 minutes": the small end of a
		// range takes the style of the large one.
		if j := rangeStart(text, toks, i, last); j >= 0 {
			small, _ := tokenWords(text, toks[j])
			if p, ok := parseGrammar(small); ok && p.decimals == "" && !p.ordinal {
				lead, core, trail := splitCore(text, toks[j], toks[j])
				b.WriteString(text[last:toks[j].start])
				b.WriteString(lead)
				b.WriteString(strconv.Itoa(p.value))
				b.WriteString(trail)
				reps = append(reps, Replacement{Span: core, Term: strconv.Itoa(p.value), Exact: true})
				last = toks[j].end
			}
		}
		lead, core, trail := splitCore(text, toks[i], toks[i+consumed-1])
		b.WriteString(text[last:toks[i].start])
		b.WriteString(lead)
		b.WriteString(out)
		b.WriteString(trail)
		reps = append(reps, Replacement{Span: core, Term: out, Exact: true})
		last = toks[i+consumed-1].end
		changed = true
		i += consumed
	}
	if !changed {
		return text, nil
	}
	b.WriteString(text[last:])
	return b.String(), reps
}

// rangeStart returns the index of a lone small number two tokens before i,
// joined to it by "to", "or" or "through", or -1. Tokens before last were
// already written and are never revisited.
func rangeStart(text string, toks []token, i, last int) int {
	j := i - 2
	if j < 0 || toks[j].start < last || toks[j].boundary || toks[i-1].boundary {
		return -1
	}
	switch toks[i-1].key {
	case "to", "or", "through":
	default:
		return -1
	}
	ws, ok := tokenWords(text, toks[j])
	if !ok || len(ws) != 1 || ws[0].kind != wordUnit || ws[0].val == 0 {
		return -1
	}
	if j > 0 && !toks[j-1].boundary {
		if prev, ok := tokenWords(text, toks[j-1]); ok && len(prev) > 0 && !isConnector(prev[0]) {
			return -1 // part of a longer number, e.g. "twenty five to thirty"
		}
	}
	return j
}

// numberRun returns how many tokens from i form one spoken number, and its
// words. Connectors are included only when a number word follows them, and
// an article only when a scale word follows. A clause boundary ends the run.
func numberRun(text string, toks []token, i int) (int, []numWord) {
	var words []numWord
	n := 0
	pendingConnectors := 0
	for j := i; j < len(toks); j++ {
		ws, ok := tokenWords(text, toks[j])
		if !ok {
			break
		}
		if len(ws) == 1 && isConnector(ws[0]) {
			if n == 0 && ws[0].kind != wordArticle {
				break
			}
			if n == 0 && ws[0].kind == wordArticle {
				if j+1 >= len(toks) || toks[j].boundary {
					break
				}
				next, ok := tokenWords(text, toks[j+1])
				if !ok || len(next) != 1 || next[0].kind != wordScale {
					break
				}
			}
			words = append(words, ws[0])
			pendingConnectors++
		} else {
			words = append(words, ws...)
			pendingConnectors = 0
			n = j - i + 1
		}
		if toks[j].boundary {
			break
		}
	}
	if pendingConnectors > 0 {
		words = words[:len(words)-pendingConnectors]
	}
	return n, words
}

type parsed struct {
	value    int
	decimals string // digits after the point, "" when none
	ordinal  bool
	year     bool
	scale    string // "million" or "billion" when the number is best read with it
	mantissa string // "6" or "2.5" when scale is set
	article  bool   // began with "a"
	count    int    // number words, connectors excluded
}

// parseNumber applies the ordinary grammar, then the year reading when that
// fails. The second value is false when the words do not read as one number.
func parseNumber(words []numWord) (parsed, bool) {
	if p, ok := parseGrammar(words); ok {
		return p, true
	}
	return parseYear(words)
}

func parseGrammar(words []numWord) (parsed, bool) {
	var p parsed
	if len(words) == 0 {
		return p, false
	}
	if words[0].kind == wordArticle {
		p.article = true
		words = words[1:]
		if len(words) == 0 || words[0].kind != wordScale {
			return p, false
		}
	}
	total, current := 0, 0
	lastKind := 0
	lastScale := 0
	inDecimal := false
	for idx, w := range words {
		if inDecimal {
			if w.kind == wordUnit && w.val < 10 {
				p.decimals += strconv.Itoa(w.val)
				p.count++
				continue
			}
			if w.kind == wordScale && w.val >= 1000000 && idx == len(words)-1 && p.decimals != "" {
				p.scale = w.raw
				p.mantissa = strconv.Itoa(total+current) + "." + p.decimals
				p.decimals = ""
				p.count++
				continue
			}
			return p, false
		}
		switch w.kind {
		case wordUnit, wordOrdinal:
			if lastKind == wordUnit || lastKind == wordOrdinal || (lastKind == wordTens && (w.val >= 10 || w.val == 0)) {
				return p, false
			}
			if w.kind == wordOrdinal {
				if idx != len(words)-1 {
					return p, false
				}
				p.ordinal = true
			}
			if lastKind == wordTens {
				current += w.val
			} else {
				if w.kind == wordOrdinal && w.val >= 100 {
					if current == 0 {
						return p, false
					}
					current *= w.val
				} else {
					current += w.val
				}
			}
			p.count++
		case wordTens:
			if lastKind == wordUnit || lastKind == wordTens || lastKind == wordOrdinal {
				return p, false
			}
			current += w.val
			p.count++
		case wordScale:
			if w.val == 100 {
				if current == 0 {
					if !p.article || idx != 0 {
						return p, false
					}
					current = 100
				} else if current >= 100 {
					return p, false
				} else {
					current *= 100
				}
			} else {
				if current == 0 {
					if !(p.article && idx == 0) {
						return p, false
					}
					current = 1
				}
				if lastScale != 0 && w.val >= lastScale {
					return p, false
				}
				lastScale = w.val
				total += current * w.val
				current = 0
				if w.val >= 1000000 && idx == len(words)-1 {
					p.scale = w.raw
				}
			}
			p.count++
		case wordAnd:
			if lastKind != wordScale || idx == len(words)-1 {
				return p, false
			}
		case wordPoint:
			if idx == 0 || idx == len(words)-1 {
				return p, false
			}
			inDecimal = true
		default:
			return p, false
		}
		lastKind = w.kind
	}
	p.value = total + current
	if p.scale != "" && p.mantissa == "" {
		mant := p.value / scaleWords[p.scale]
		if p.decimals != "" {
			p.mantissa = strconv.Itoa(mant) + "." + p.decimals
			p.decimals = ""
		} else if p.value%scaleWords[p.scale] == 0 && mant < 1000 {
			p.mantissa = strconv.Itoa(mant)
		} else {
			p.scale = ""
		}
	}
	return p, true
}

// parseYear reads "nineteen ninety nine", "twenty twenty two", "twenty oh
// eight": a leading nineteen or twenty followed by a two-digit group.
func parseYear(words []numWord) (parsed, bool) {
	var p parsed
	if len(words) < 2 || (words[0].kind != wordUnit && words[0].kind != wordTens) || (words[0].val != 19 && words[0].val != 20) {
		return p, false
	}
	rest := words[1:]
	if rest[0].kind == wordOh {
		if len(rest) != 2 || rest[1].kind != wordUnit || rest[1].val >= 10 {
			return p, false
		}
		p.value = words[0].val*100 + rest[1].val
		p.year = true
		p.count = 3
		return p, true
	}
	q, ok := parseGrammar(rest)
	if !ok || q.ordinal || q.decimals != "" || q.scale != "" || q.value >= 100 {
		return p, false
	}
	if len(rest) == 1 && rest[0].kind == wordUnit && rest[0].val < 10 {
		return p, false // "twenty one" is 21
	}
	p.value = words[0].val*100 + q.value
	p.year = true
	p.count = 1 + q.count
	return p, true
}

// renderNumber decides whether the run should be digits and formats it.
func renderNumber(words []numWord, unit string) (string, bool) {
	p, ok := parseNumber(words)
	if !ok {
		return "", false
	}
	switch {
	case p.year:
		return strconv.Itoa(p.value), true
	case p.article && p.count <= 1 && unit == "":
		return "", false // "a hundred people", "a billion contexts" stay words
	case p.decimals != "" || p.mantissa != "":
	case unit != "":
	case p.ordinal:
		if p.value < 10 {
			return "", false
		}
	case p.value < 10 && p.count <= 1:
		return "", false // "two years", "one thing"
	}
	var s string
	switch {
	case p.mantissa != "":
		s = p.mantissa + " " + p.scale
	case p.decimals != "":
		s = strconv.Itoa(p.value) + "." + p.decimals
	default:
		s = groupDigits(p.value, unit == "" && p.value >= 1900 && p.value <= 2099)
	}
	if p.ordinal {
		s += ordinalSuffix(p.value)
	}
	switch unit {
	case "$":
		s = "$" + s
	case "%":
		s += "%"
	}
	return s, true
}

func groupDigits(v int, plain bool) string {
	s := strconv.Itoa(v)
	if plain || v < 1000 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func ordinalSuffix(v int) string {
	if v%100 >= 11 && v%100 <= 13 {
		return "th"
	}
	switch v % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}
