package correct

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

// common_words.txt.gz is derived from SCOWL; see SCOWL-COPYRIGHT.txt.
//
//go:embed common_words.txt.gz
var commonWordsGz []byte

var (
	commonOnce sync.Once
	commonSet  map[string]struct{}
)

// IsCommon reports whether key is an everyday English word or a common proper
// name. Common words are never fuzzy-replaced and never fuzzy-matched as terms:
// a misheard rare name almost always surfaces as a rare token, while replacing
// real words is how a matcher rewrites "worked" into a product name.
func IsCommon(key string) bool {
	commonOnce.Do(loadCommon)
	_, ok := commonSet[key]
	return ok
}

func loadCommon() {
	zr, err := gzip.NewReader(bytes.NewReader(commonWordsGz))
	if err != nil {
		panic("correct: embedded common_words.txt.gz is not gzip: " + err.Error())
	}
	commonSet = make(map[string]struct{}, 40000)
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if w := sc.Text(); w != "" {
			commonSet[w] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		panic("correct: embedded common_words.txt.gz is truncated: " + err.Error())
	}
}

// Key normalizes a phrase to its match key: lower-cased ASCII letters and
// digits only. "Sam antha", "sam-antha" and "SAMANTHA" share one key.
func Key(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isStopKey reports whether a token is too short or too common to take part
// in a fuzzy replacement.
func isStopKey(key string) bool {
	return len(key) < minStopKeyLen || IsCommon(key)
}

// Levenshtein returns the edit distance between two ASCII keys.
func Levenshtein(a, b string) int {
	var st scanState
	return st.lev(a, b)
}

// Soundex returns the four-character American Soundex code of a key's letters,
// or "" when the key has no letters. One departure from the standard: an
// initial vowel is folded to A, because a transcriber that hears "Alama" for
// Ollama changed the vowel, not the name, and the code must say so.
func Soundex(key string) string {
	var out [4]byte
	n := 0
	last := byte(0)
	for i := 0; i < len(key) && n < 4; i++ {
		c := key[i]
		if c < 'a' || c > 'z' {
			continue
		}
		code := soundexCode(c)
		if n == 0 {
			out[0] = c - 'a' + 'A'
			if strings.IndexByte("aeiou", c) >= 0 {
				out[0] = 'A'
			}
			n = 1
			last = code
			continue
		}
		if code != 0 && code != last {
			out[n] = code
			n++
		}
		if c != 'h' && c != 'w' {
			last = code
		}
	}
	if n == 0 {
		return ""
	}
	for ; n < 4; n++ {
		out[n] = '0'
	}
	return string(out[:])
}

func soundexCode(c byte) byte {
	switch c {
	case 'b', 'f', 'p', 'v':
		return '1'
	case 'c', 'g', 'j', 'k', 'q', 's', 'x', 'z':
		return '2'
	case 'd', 't':
		return '3'
	case 'l':
		return '4'
	case 'm', 'n':
		return '5'
	case 'r':
		return '6'
	}
	return 0
}
