// Package hfbpe encodes text with a HuggingFace byte-level BPE tokenizer,
// reading the model's own tokenizer.json, in pure Go and with no cgo.
//
// It implements the subset of the tokenizers format that the Audio8 TTS
// checkpoint declares — a BPE model, an added-token vocabulary, and a
// pre-tokenizer sequence of Punctuation, Split and ByteLevel steps — and
// nothing else. Every construct outside that subset is rejected when the file
// is loaded, with the offending field named: a tokenizer that quietly splits
// differently from the reference produces silently wrong token ids, which for
// a TTS front end means confident, wrong speech.
package hfbpe

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Tokenizer encodes text into the model's token ids. It is immutable after
// Load and safe for concurrent use.
type Tokenizer struct {
	vocab  map[string]uint32
	merges map[mergePair]mergeRule
	added  *addedVocab
	steps  []preStep
}

// preStep is one pre-tokenizer stage: pieces in, pieces out.
type preStep func([]string) []string

// Load reads a tokenizer.json from disk.
func Load(path string) (*Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hfbpe: read tokenizer: %w", err)
	}
	t, err := New(raw)
	if err != nil {
		return nil, fmt.Errorf("hfbpe: %s: %w", path, err)
	}
	return t, nil
}

// New parses tokenizer.json bytes.
func New(raw []byte) (*Tokenizer, error) {
	var spec tokenizerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse tokenizer.json: %w", err)
	}
	if err := spec.checkUnsupported(); err != nil {
		return nil, err
	}
	t := &Tokenizer{vocab: make(map[string]uint32, len(spec.Model.Vocab))}
	for token, id := range spec.Model.Vocab {
		t.vocab[token] = id
	}
	if err := t.checkByteAlphabet(); err != nil {
		return nil, err
	}
	var err error
	if t.merges, err = buildMerges(spec.Model.Merges, t.vocab); err != nil {
		return nil, err
	}
	added, err := buildAddedTokens(spec.AddedTokens)
	if err != nil {
		return nil, err
	}
	t.added = newAddedVocab(added)
	if t.steps, err = buildPreTokenizer(spec.PreTokenizer); err != nil {
		return nil, fmt.Errorf("pre_tokenizer: %w", err)
	}
	return t, nil
}

// Encode turns text into token ids.
func (t *Tokenizer) Encode(text string) ([]int64, error) {
	// The reference works on Rust &str and cannot be handed anything else, so
	// there is no reference behaviour to match for invalid UTF-8. Byte-level
	// encoding it anyway would need the alphabet runes for 0xC0/0xC1/0xF5..FF,
	// which this vocabulary does not carry.
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("hfbpe: input is not valid UTF-8")
	}
	out := make([]int64, 0, len(text)/2+1)
	pos := 0
	for pos < len(text) {
		start, end, id, ok := t.added.find(text, pos)
		if !ok {
			break
		}
		if start > pos {
			if err := t.encodeSegment(text[pos:start], &out); err != nil {
				return nil, err
			}
		}
		out = append(out, id)
		pos = end
	}
	if pos < len(text) {
		if err := t.encodeSegment(text[pos:], &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// encodeSegment runs the pre-tokenizer chain and the BPE over a stretch of
// text that carries no added tokens.
func (t *Tokenizer) encodeSegment(s string, out *[]int64) error {
	pieces := []string{s}
	for _, step := range t.steps {
		pieces = step(pieces)
	}
	buf := make([]uint32, 0, 64)
	for _, piece := range pieces {
		buf = buf[:0]
		for _, r := range piece {
			id, ok := t.vocab[string(r)]
			if !ok {
				return fmt.Errorf("hfbpe: no vocabulary entry for %q in pre-token %q", r, piece)
			}
			buf = append(buf, id)
		}
		for _, id := range t.merge(buf) {
			*out = append(*out, int64(id))
		}
	}
	return nil
}

// checkByteAlphabet fails the load when a byte-level alphabet rune that valid
// UTF-8 can reach has no vocabulary entry. The reference silently drops such a
// character (its unk_token is null), which would mean losing audio rather than
// reporting a broken vocabulary.
func (t *Tokenizer) checkByteAlphabet() error {
	var missing []string
	for b := range 256 {
		if !utf8ReachableBytes[b] {
			continue
		}
		if _, ok := t.vocab[string(byteToRune[b])]; !ok {
			missing = append(missing, fmt.Sprintf("0x%02X", b))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("vocab is missing the byte-level alphabet for %d byte(s): %s",
			len(missing), strings.Join(missing, ", "))
	}
	return nil
}

func buildMerges(raw []json.RawMessage, vocab map[string]uint32) (map[mergePair]mergeRule, error) {
	merges := make(map[mergePair]mergeRule, len(raw))
	for i, entry := range raw {
		left, right, err := decodeMerge(entry)
		if err != nil {
			return nil, fmt.Errorf("model.merges[%d]: %w", i, err)
		}
		a, ok := vocab[left]
		if !ok {
			return nil, fmt.Errorf("model.merges[%d]: %q is not in the vocab", i, left)
		}
		b, ok := vocab[right]
		if !ok {
			return nil, fmt.Errorf("model.merges[%d]: %q is not in the vocab", i, right)
		}
		id, ok := vocab[left+right]
		if !ok {
			return nil, fmt.Errorf("model.merges[%d]: merged token %q is not in the vocab", i, left+right)
		}
		pair := mergePair{a, b}
		if _, dup := merges[pair]; dup {
			// The reference keeps the first rank for a repeated pair; a file
			// with duplicates is malformed either way, so say so.
			return nil, fmt.Errorf("model.merges[%d]: pair (%q, %q) appears twice", i, left, right)
		}
		merges[pair] = mergeRule{rank: int32(i), id: id}
	}
	return merges, nil
}

// decodeMerge accepts both on-disk shapes: the ["a", "b"] pairs current
// tokenizers writes, and the legacy "a b" line.
func decodeMerge(entry json.RawMessage) (string, string, error) {
	var pair []string
	if err := json.Unmarshal(entry, &pair); err == nil {
		if len(pair) != 2 {
			return "", "", fmt.Errorf("want 2 parts, got %d", len(pair))
		}
		return pair[0], pair[1], nil
	}
	var line string
	if err := json.Unmarshal(entry, &line); err != nil {
		return "", "", fmt.Errorf("want [\"a\", \"b\"] or \"a b\", got %s", entry)
	}
	parts := strings.Split(line, " ")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("want exactly one space in %q", line)
	}
	return parts[0], parts[1], nil
}

func buildAddedTokens(specs []addedTokenSpec) (map[string]int64, error) {
	out := make(map[string]int64, len(specs))
	for i, tok := range specs {
		switch {
		case tok.Content == "":
			return nil, fmt.Errorf("added_tokens[%d]: empty content", i)
		case tok.SingleWord:
			return nil, fmt.Errorf("added_tokens[%d] (%q): single_word is not implemented", i, tok.Content)
		case tok.LStrip:
			return nil, fmt.Errorf("added_tokens[%d] (%q): lstrip is not implemented", i, tok.Content)
		case tok.RStrip:
			return nil, fmt.Errorf("added_tokens[%d] (%q): rstrip is not implemented", i, tok.Content)
		case tok.Normalized:
			// Normalized added tokens are matched after normalization, in a
			// second pass with its own precedence; unimplemented, and moot
			// while checkUnsupported requires a null normalizer.
			return nil, fmt.Errorf("added_tokens[%d] (%q): normalized is not implemented", i, tok.Content)
		}
		if prev, dup := out[tok.Content]; dup && prev != tok.ID {
			return nil, fmt.Errorf("added_tokens[%d]: %q maps to both %d and %d", i, tok.Content, prev, tok.ID)
		}
		out[tok.Content] = tok.ID
	}
	return out, nil
}

func buildPreTokenizer(raw json.RawMessage) ([]preStep, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var spec preTokenizerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	switch spec.Type {
	case "Sequence":
		var steps []preStep
		for i, sub := range spec.PreTokenizers {
			s, err := buildPreTokenizer(sub)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			steps = append(steps, s...)
		}
		return steps, nil

	case "Punctuation":
		behavior := spec.Behavior
		if behavior == "" {
			behavior = "Isolated" // the reference's default
		}
		b, err := parseBehavior(behavior)
		if err != nil {
			return nil, fmt.Errorf(`pre-tokenizer "Punctuation": %w`, err)
		}
		m := runeMatcher{ok: isPunctuation}
		return []preStep{splitStep(m, b, false)}, nil

	case "Split":
		if spec.Behavior == "" {
			return nil, fmt.Errorf(`pre-tokenizer "Split": behavior is missing`)
		}
		b, err := parseBehavior(spec.Behavior)
		if err != nil {
			return nil, fmt.Errorf(`pre-tokenizer "Split": %w`, err)
		}
		if spec.Pattern == nil {
			return nil, fmt.Errorf(`pre-tokenizer "Split": pattern is missing`)
		}
		var m matcher
		switch {
		case spec.Pattern.Regex != nil:
			if m, err = newAltMatcher(*spec.Pattern.Regex); err != nil {
				return nil, fmt.Errorf(`pre-tokenizer "Split": %w`, err)
			}
		case spec.Pattern.String != nil:
			m = literalMatcher{lit: *spec.Pattern.String}
		default:
			return nil, fmt.Errorf(`pre-tokenizer "Split": pattern is neither Regex nor String`)
		}
		return []preStep{splitStep(m, b, spec.Invert)}, nil

	case "ByteLevel":
		if spec.AddPrefixSpace == nil {
			return nil, fmt.Errorf(`pre-tokenizer "ByteLevel": add_prefix_space is missing`)
		}
		// use_regex defaults to true in the reference when the field is absent.
		if spec.UseRegex == nil || *spec.UseRegex {
			return nil, fmt.Errorf(`pre-tokenizer "ByteLevel": use_regex is not implemented (this tokenizer must declare false)`)
		}
		addPrefixSpace := *spec.AddPrefixSpace
		return []preStep{func(pieces []string) []string {
			out := make([]string, 0, len(pieces))
			for _, p := range pieces {
				if addPrefixSpace && !strings.HasPrefix(p, " ") {
					p = " " + p
				}
				out = append(out, byteLevelEncode(p))
			}
			return out
		}}, nil

	default:
		return nil, fmt.Errorf("pre-tokenizer type %q is not implemented", spec.Type)
	}
}

func splitStep(m matcher, b splitBehavior, invert bool) preStep {
	return func(pieces []string) []string {
		out := make([]string, 0, len(pieces)+4)
		for _, p := range pieces {
			out = append(out, splitString(p, m, b, invert)...)
		}
		return out
	}
}
