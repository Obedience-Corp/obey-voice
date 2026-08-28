package hfbpe

import (
	"encoding/json"
	"fmt"
)

// tokenizerSpec is the on-disk tokenizer.json. Fields this package does not
// implement are kept as raw JSON so checkUnsupported can refuse them by name
// instead of ignoring them.
type tokenizerSpec struct {
	Truncation    json.RawMessage  `json:"truncation"`
	Padding       json.RawMessage  `json:"padding"`
	AddedTokens   []addedTokenSpec `json:"added_tokens"`
	Normalizer    json.RawMessage  `json:"normalizer"`
	PreTokenizer  json.RawMessage  `json:"pre_tokenizer"`
	PostProcessor json.RawMessage  `json:"post_processor"`
	Model         modelSpec        `json:"model"`
}

// addedTokenSpec omits "special": the reference extracts added tokens from the
// input whether or not they are special, and this package never adds any.
type addedTokenSpec struct {
	ID         int64  `json:"id"`
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	LStrip     bool   `json:"lstrip"`
	RStrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
}

type modelSpec struct {
	Type                    string            `json:"type"`
	Dropout                 json.RawMessage   `json:"dropout"`
	UnkToken                json.RawMessage   `json:"unk_token"`
	ContinuingSubwordPrefix json.RawMessage   `json:"continuing_subword_prefix"`
	EndOfWordSuffix         json.RawMessage   `json:"end_of_word_suffix"`
	FuseUnk                 bool              `json:"fuse_unk"`
	ByteFallback            bool              `json:"byte_fallback"`
	IgnoreMerges            bool              `json:"ignore_merges"`
	Vocab                   map[string]uint32 `json:"vocab"`
	Merges                  []json.RawMessage `json:"merges"`
}

type preTokenizerSpec struct {
	Type string `json:"type"`

	// Sequence
	PreTokenizers []json.RawMessage `json:"pretokenizers"`

	// Punctuation, Split
	Behavior string       `json:"behavior"`
	Pattern  *patternSpec `json:"pattern"`
	Invert   bool         `json:"invert"`

	// ByteLevel. Pointers because the reference's defaults for the missing
	// fields are not Go's zero values.
	AddPrefixSpace *bool `json:"add_prefix_space"`
	UseRegex       *bool `json:"use_regex"`
}

type patternSpec struct {
	String *string `json:"String"`
	Regex  *string `json:"Regex"`
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// checkUnsupported refuses every declared behaviour this package does not
// implement. "decoder" is the one field deliberately ignored: it is only
// consulted when turning ids back into text, which this package never does.
func (s *tokenizerSpec) checkUnsupported() error {
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"truncation", s.Truncation},
		{"padding", s.Padding},
		{"normalizer", s.Normalizer},
		{"post_processor", s.PostProcessor},
	} {
		if !isJSONNull(f.raw) {
			return fmt.Errorf("%s is not implemented (declared: %s)", f.name, summarize(f.raw))
		}
	}
	if s.Model.Type != "BPE" {
		return fmt.Errorf("model.type %q is not implemented (want BPE)", s.Model.Type)
	}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"model.dropout", s.Model.Dropout},
		{"model.unk_token", s.Model.UnkToken},
		{"model.continuing_subword_prefix", s.Model.ContinuingSubwordPrefix},
		{"model.end_of_word_suffix", s.Model.EndOfWordSuffix},
	} {
		if !isJSONNull(f.raw) {
			return fmt.Errorf("%s is not implemented (declared: %s)", f.name, summarize(f.raw))
		}
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"model.fuse_unk", s.Model.FuseUnk},
		{"model.byte_fallback", s.Model.ByteFallback},
		{"model.ignore_merges", s.Model.IgnoreMerges},
	} {
		if f.set {
			return fmt.Errorf("%s is not implemented (declared true)", f.name)
		}
	}
	if len(s.Model.Vocab) == 0 {
		return fmt.Errorf("model.vocab is empty")
	}
	return nil
}

// summarize keeps an error message readable when the offending field is a
// large object.
func summarize(raw json.RawMessage) string {
	const max = 120
	if len(raw) <= max {
		return string(raw)
	}
	return string(raw[:max]) + "..."
}
