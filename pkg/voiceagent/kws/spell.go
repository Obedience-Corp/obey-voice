package kws

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// ErrUnspellable marks a name the keyword model has no way to write: its
// vocabulary is upper-case English letters and the apostrophe.
var ErrUnspellable = errors.New("kws: name cannot be spelled for the keyword model")

// CheckName reports whether the keyword model can be asked to listen for
// name. It needs no model on disk, so a host can validate a name at
// configuration time. Digits ("R2D2") and letters outside A-Z ("Zoë") fail;
// write such a name the way it sounds ("Artoo Detoo", "Zoey") as an alias.
func CheckName(name string) error {
	_, err := normalize(name)
	return err
}

// normalize upper-cases name and returns its words. Sentencepiece would do
// the same case-preserving split on spaces; upper-casing is ours, because the
// model was trained on upper-case text only.
func normalize(name string) ([]string, error) {
	// A hyphen is not spoken, so "Mary-Jane" is listened for as two words.
	words := strings.Fields(strings.ReplaceAll(strings.ToUpper(name), "-", " "))
	if len(words) == 0 {
		return nil, fmt.Errorf("%w: it is empty", ErrUnspellable)
	}
	for _, word := range words {
		letters := 0
		for _, r := range word {
			switch {
			case r >= 'A' && r <= 'Z':
				letters++
			case r == '\'':
			default:
				return nil, fmt.Errorf("%w: %q has %q; only English letters, spaces, hyphens and apostrophes can be spelled", ErrUnspellable, name, r)
			}
		}
		if letters == 0 {
			return nil, fmt.Errorf("%w: %q has a word with no letters", ErrUnspellable, name)
		}
	}
	return words, nil
}

// wordMark is sentencepiece's word-boundary marker, U+2581.
const wordMark = "▁"

// speller writes names as the token lines the keyword spotter takes. The
// native library does not tokenize keywords itself, and it ends the process on
// a line holding a token it does not know, so every line must come from here.
type speller struct {
	scores  map[string]float64
	longest int
}

// loadSpeller reads the pieces and scores out of the model's sentencepiece
// file. Only the two fields needed are decoded: ModelProto.pieces (field 1),
// each with piece (1), score (2) and type (3). Normal pieces alone take part.
func loadSpeller(path string) (*speller, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &speller{scores: map[string]float64{}}
	err = eachField(raw, func(field int, wire int, val []byte, num uint64) error {
		if field != 1 || wire != 2 {
			return nil
		}
		var piece string
		var score float64
		kind := uint64(1)
		if err := eachField(val, func(field int, wire int, val []byte, num uint64) error {
			switch {
			case field == 1 && wire == 2:
				piece = string(val)
			case field == 2 && wire == 5:
				score = float64(math.Float32frombits(uint32(num)))
			case field == 3 && wire == 0:
				kind = num
			}
			return nil
		}); err != nil {
			return err
		}
		if kind == 1 && piece != "" {
			s.scores[piece] = score
			s.longest = max(s.longest, len(piece))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(s.scores) == 0 {
		return nil, fmt.Errorf("read %s: no vocabulary found", path)
	}
	return s, nil
}

// eachField walks one protobuf message. num carries varint and fixed values,
// val carries length-delimited ones.
func eachField(b []byte, visit func(field int, wire int, val []byte, num uint64) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errors.New("malformed field key")
		}
		b = b[n:]
		field, wire := int(key>>3), int(key&7)
		var val []byte
		var num uint64
		switch wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return errors.New("malformed varint")
			}
			num, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return errors.New("truncated fixed64")
			}
			num, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			size, n := binary.Uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) {
				return errors.New("truncated field")
			}
			val, b = b[n:n+int(size)], b[n+int(size):]
		case 5:
			if len(b) < 4 {
				return errors.New("truncated fixed32")
			}
			num, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return fmt.Errorf("unsupported wire type %d", wire)
		}
		if err := visit(field, wire, val, num); err != nil {
			return err
		}
	}
	return nil
}

// spell returns the tokens of name, space separated: the highest-scoring split
// of each word into vocabulary pieces, which is what a unigram sentencepiece
// model produces and what the keyword model was trained to hear.
func (s *speller) spell(name string) (string, error) {
	words, err := normalize(name)
	if err != nil {
		return "", err
	}
	var tokens []string
	for _, word := range words {
		pieces, err := s.split(wordMark + word)
		if err != nil {
			return "", fmt.Errorf("%w: %q", err, name)
		}
		tokens = append(tokens, pieces...)
	}
	return strings.Join(tokens, " "), nil
}

func (s *speller) split(text string) ([]string, error) {
	// best[i] is the best score of text[:i]; from[i] is where its last piece starts.
	best := make([]float64, len(text)+1)
	from := make([]int, len(text)+1)
	for i := 1; i <= len(text); i++ {
		best[i], from[i] = math.Inf(-1), -1
		for j := max(0, i-s.longest); j < i; j++ {
			if from[j] < 0 && j != 0 {
				continue
			}
			score, ok := s.scores[text[j:i]]
			if ok && best[j]+score > best[i] {
				best[i], from[i] = best[j]+score, j
			}
		}
	}
	if from[len(text)] < 0 {
		return nil, ErrUnspellable
	}
	var pieces []string
	for i := len(text); i > 0; i = from[i] {
		pieces = append([]string{text[from[i]:i]}, pieces...)
	}
	return pieces, nil
}
