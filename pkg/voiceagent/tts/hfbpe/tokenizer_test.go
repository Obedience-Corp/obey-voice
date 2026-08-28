package hfbpe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// parityDoc is the shape gen_parity.py writes.
type parityDoc struct {
	Cases []struct {
		Text string  `json:"text"`
		IDs  []int64 `json:"ids"`
	} `json:"cases"`
}

func loadParity(t *testing.T, name string) parityDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var doc parityDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(doc.Cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
	return doc
}

// TestMiniTokenizerParity is the always-on parity gate. mini_tokenizer.json is
// a real tokenizers-trained BPE carrying the Audio8 checkpoint's exact
// pre-tokenizer sequence and added-token shapes, and every expected id in
// mini_parity.json came out of the reference implementation. It runs without
// the 5.8 MB checkpoint tokenizer, which is not vendored.
func TestMiniTokenizerParity(t *testing.T) {
	tok, err := Load(filepath.Join("testdata", "mini_tokenizer.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range loadParity(t, "mini_parity.json").Cases {
		t.Run(fmt.Sprintf("%q", tc.Text), func(t *testing.T) {
			got, err := tok.Encode(tc.Text)
			if err != nil {
				t.Fatalf("Encode(%q) error = %v", tc.Text, err)
			}
			if !slices.Equal(got, tc.IDs) {
				t.Fatalf("Encode(%q)\n got %v\nwant %v", tc.Text, got, tc.IDs)
			}
		})
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	tok, err := Load(filepath.Join("testdata", "mini_tokenizer.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const text = "Hello, world! 123 <|im_start|>ab abcd\n\n日本語"
	first, err := tok.Encode(text)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for range 32 {
		got, err := tok.Encode(text)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if !slices.Equal(got, first) {
			t.Fatalf("Encode is not deterministic: %v then %v", first, got)
		}
	}
}

// TestPunctuationTableMatchesReference pins punctuation.go to the set swept
// from the reference. Go's unicode.P and the reference's table disagree on 129
// code points; splitting on the wrong set means different ids.
func TestPunctuationTableMatchesReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "punctuation_codepoints.json"))
	if err != nil {
		t.Fatalf("read punctuation_codepoints.json: %v", err)
	}
	var doc struct {
		Ranges [][2]rune `json:"ranges"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse punctuation_codepoints.json: %v", err)
	}
	if len(doc.Ranges) == 0 {
		t.Fatal("no ranges in the reference sweep")
	}
	want := make(map[rune]bool)
	for _, r := range doc.Ranges {
		for cp := r[0]; cp <= r[1]; cp++ {
			want[cp] = true
		}
	}
	var mismatches int
	for cp := rune(0); cp <= 0x10FFFF; cp++ {
		// The sweep cannot probe 'A'/'a' (they are the probe's own context) or
		// the surrogate range (not scalar values).
		if cp == 'A' || cp == 'a' || (cp >= 0xD800 && cp <= 0xDFFF) {
			continue
		}
		if isPunctuation(cp) != want[cp] {
			if mismatches < 10 {
				t.Errorf("isPunctuation(U+%04X) = %v, reference says %v", cp, !want[cp], want[cp])
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d code points disagree with the reference sweep", mismatches)
	}
}

// --- load-time rejection ---------------------------------------------------

// minimalSpec is the smallest tokenizer.json this package accepts: a full
// byte-level alphabet, one merge, one added token, and a pre-tokenizer
// sequence shaped like the checkpoint's. Tests mutate one field at a time.
func minimalSpec(mutate func(doc map[string]any)) []byte {
	vocab := map[string]any{}
	next := 0
	for b := range 256 {
		if !utf8ReachableBytes[b] {
			continue
		}
		vocab[string(byteToRune[b])] = next
		next++
	}
	vocab["ab"] = next
	doc := map[string]any{
		"version":    "1.0",
		"truncation": nil,
		"padding":    nil,
		"added_tokens": []any{map[string]any{
			"id": 900, "content": "<|x|>", "single_word": false,
			"lstrip": false, "rstrip": false, "normalized": false, "special": true,
		}},
		"normalizer": nil,
		"pre_tokenizer": map[string]any{
			"type": "Sequence",
			"pretokenizers": []any{
				map[string]any{"type": "Punctuation", "behavior": "Contiguous"},
				map[string]any{"type": "Split", "pattern": map[string]any{"Regex": `\p{N}| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+|[\p{L}\p{M}]+`}, "behavior": "Isolated", "invert": false},
				map[string]any{"type": "ByteLevel", "add_prefix_space": false, "trim_offsets": true, "use_regex": false},
			},
		},
		"post_processor": nil,
		"decoder":        map[string]any{"type": "ByteLevel", "add_prefix_space": true, "trim_offsets": true, "use_regex": true},
		"model": map[string]any{
			"type": "BPE", "dropout": nil, "unk_token": nil,
			"continuing_subword_prefix": nil, "end_of_word_suffix": nil,
			"fuse_unk": false, "byte_fallback": false, "ignore_merges": false,
			"vocab": vocab, "merges": []any{[]string{"a", "b"}},
		},
	}
	if mutate != nil {
		mutate(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

func model(doc map[string]any) map[string]any { return doc["model"].(map[string]any) }

func TestNewRejectsUnsupportedTokenizers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"truncation", func(d map[string]any) {
			d["truncation"] = map[string]any{"max_length": 8}
		}, "truncation is not implemented"},
		{"padding", func(d map[string]any) {
			d["padding"] = map[string]any{"strategy": "BatchLongest"}
		}, "padding is not implemented"},
		{"normalizer", func(d map[string]any) {
			d["normalizer"] = map[string]any{"type": "NFKC"}
		}, "normalizer is not implemented"},
		{"post_processor", func(d map[string]any) {
			d["post_processor"] = map[string]any{"type": "TemplateProcessing"}
		}, "post_processor is not implemented"},
		{"model type", func(d map[string]any) {
			model(d)["type"] = "WordPiece"
		}, `model.type "WordPiece" is not implemented`},
		{"dropout", func(d map[string]any) {
			model(d)["dropout"] = 0.1
		}, "model.dropout is not implemented"},
		{"unk token", func(d map[string]any) {
			model(d)["unk_token"] = "<unk>"
		}, "model.unk_token is not implemented"},
		{"continuing subword prefix", func(d map[string]any) {
			model(d)["continuing_subword_prefix"] = "##"
		}, "model.continuing_subword_prefix is not implemented"},
		{"end of word suffix", func(d map[string]any) {
			model(d)["end_of_word_suffix"] = "</w>"
		}, "model.end_of_word_suffix is not implemented"},
		{"fuse unk", func(d map[string]any) {
			model(d)["fuse_unk"] = true
		}, "model.fuse_unk is not implemented"},
		{"byte fallback", func(d map[string]any) {
			model(d)["byte_fallback"] = true
		}, "model.byte_fallback is not implemented"},
		{"ignore merges", func(d map[string]any) {
			model(d)["ignore_merges"] = true
		}, "model.ignore_merges is not implemented"},
		{"empty vocab", func(d map[string]any) {
			model(d)["vocab"] = map[string]any{}
		}, "model.vocab is empty"},
		{"vocab missing byte alphabet", func(d map[string]any) {
			v := model(d)["vocab"].(map[string]any)
			delete(v, string(byteToRune['a']))
			delete(v, string(byteToRune[0]))
		}, "vocab is missing the byte-level alphabet for 2 byte(s)"},
		{"merge part not in vocab", func(d map[string]any) {
			model(d)["merges"] = []any{[]string{"zzz", "b"}}
		}, `model.merges[0]: "zzz" is not in the vocab`},
		{"merged token not in vocab", func(d map[string]any) {
			model(d)["merges"] = []any{[]string{"a", "c"}}
		}, `model.merges[0]: merged token "ac" is not in the vocab`},
		{"duplicate merge", func(d map[string]any) {
			model(d)["merges"] = []any{[]string{"a", "b"}, []string{"a", "b"}}
		}, `model.merges[1]: pair ("a", "b") appears twice`},
		{"merge shape", func(d map[string]any) {
			model(d)["merges"] = []any{[]string{"a", "b", "c"}}
		}, "model.merges[0]: want 2 parts, got 3"},
		{"legacy merge with two spaces", func(d map[string]any) {
			model(d)["merges"] = []any{"a b c"}
		}, `model.merges[0]: want exactly one space in "a b c"`},
		{"added token lstrip", func(d map[string]any) {
			d["added_tokens"].([]any)[0].(map[string]any)["lstrip"] = true
		}, "lstrip is not implemented"},
		{"added token rstrip", func(d map[string]any) {
			d["added_tokens"].([]any)[0].(map[string]any)["rstrip"] = true
		}, "rstrip is not implemented"},
		{"added token single_word", func(d map[string]any) {
			d["added_tokens"].([]any)[0].(map[string]any)["single_word"] = true
		}, "single_word is not implemented"},
		{"added token normalized", func(d map[string]any) {
			d["added_tokens"].([]any)[0].(map[string]any)["normalized"] = true
		}, "normalized is not implemented"},
		{"unknown pre-tokenizer", func(d map[string]any) {
			d["pre_tokenizer"] = map[string]any{"type": "Whitespace"}
		}, `pre-tokenizer type "Whitespace" is not implemented`},
		{"unknown pre-tokenizer inside a sequence", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)
			seq["pretokenizers"] = append(seq["pretokenizers"].([]any), map[string]any{"type": "Metaspace"})
		}, `[3]: pre-tokenizer type "Metaspace" is not implemented`},
		{"bytelevel use_regex", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[2].(map[string]any)["use_regex"] = true
		}, `pre-tokenizer "ByteLevel": use_regex is not implemented`},
		{"bytelevel use_regex defaults to true", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			delete(seq[2].(map[string]any), "use_regex")
		}, `pre-tokenizer "ByteLevel": use_regex is not implemented`},
		{"bytelevel add_prefix_space missing", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			delete(seq[2].(map[string]any), "add_prefix_space")
		}, `pre-tokenizer "ByteLevel": add_prefix_space is missing`},
		{"split behavior missing", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			delete(seq[1].(map[string]any), "behavior")
		}, `pre-tokenizer "Split": behavior is missing`},
		{"split behavior not implemented", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["behavior"] = "MergedWithPrevious"
		}, `pre-tokenizer "Split": split behavior "MergedWithPrevious" is not implemented`},
		{"split behavior unknown", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["behavior"] = "Sideways"
		}, `pre-tokenizer "Split": unknown split behavior "Sideways"`},
		{"split pattern missing", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			delete(seq[1].(map[string]any), "pattern")
		}, `pre-tokenizer "Split": pattern is missing`},
		{"split pattern empty object", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{}
		}, `pre-tokenizer "Split": pattern is neither Regex nor String`},
		{"split regex with an unsupported lookahead", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `foo(?=bar)`}
		}, "Go regexp cannot compile it"},
		{"split regex with a word class", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `\w+`}
		}, `\w has different (or no) meaning`},
		{"split regex with an anchor", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `^a`}
		}, "anchor"},
		// Set operations compile in Go with a different meaning, so nothing
		// downstream would notice: this file has to refuse them at load.
		{"split regex with a class intersection", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `[\p{L}&&\p{Greek}]`}
		}, "set operation"},
		{"split regex with a class difference", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `[a-z--[aeiou]]`}
		}, "set operation"},
		{"split regex with a nested class", func(d map[string]any) {
			seq := d["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
			seq[1].(map[string]any)["pattern"] = map[string]any{"Regex": `[[a-z]&&[b]]`}
		}, "nested character class"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(minimalSpec(tc.mutate))
			if err == nil {
				t.Fatalf("New() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestCheckpointSequenceRejectsASetOperationSplit replays the review's
// end-to-end demonstration on the real pre-tokenizer sequence: swapping the
// checkpoint's fourth step ("[0-9]") for a class intersection used to load
// clean and then split differently from the reference. It must now fail at
// load, and the untouched file must still load.
func TestCheckpointSequenceRejectsASetOperationSplit(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mini_tokenizer.json"))
	if err != nil {
		t.Fatalf("read mini_tokenizer.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse mini_tokenizer.json: %v", err)
	}
	if _, err := New(raw); err != nil {
		t.Fatalf("New() on the untouched file = %v, want it to load", err)
	}

	steps := doc["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
	digits := steps[3].(map[string]any)
	if got := digits["pattern"].(map[string]any)["Regex"]; got != "[0-9]" {
		t.Fatalf("pre_tokenizer[3] pattern = %v, want the checkpoint's [0-9] split", got)
	}
	digits["pattern"] = map[string]any{"Regex": `[\p{L}&&\p{Greek}]`}
	patched, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(patched)
	if err == nil {
		t.Fatal("New() error = nil, want the class intersection refused")
	}
	if !strings.Contains(err.Error(), "set operation") {
		t.Fatalf("New() error = %q, want it to name the set operation", err)
	}
}

func TestNewAcceptsTheMinimalSpec(t *testing.T) {
	tok, err := New(minimalSpec(nil))
	if err != nil {
		t.Fatalf("New() error = %v, want the minimal spec to load", err)
	}
	ids, err := tok.Encode("ab<|x|>")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(ids) != 2 || ids[1] != 900 {
		t.Fatalf("Encode(%q) = %v, want the merged pair then the added token id 900", "ab<|x|>", ids)
	}
}

func TestNewRejectsMalformedJSON(t *testing.T) {
	for _, raw := range []string{"", "{", "null", `{"model": 3}`, `[]`} {
		if _, err := New([]byte(raw)); err == nil {
			t.Errorf("New(%q) error = nil, want a parse error", raw)
		}
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "tokenizer.json"))
	if err == nil {
		t.Fatal("Load() error = nil, want a read error")
	}
	if !strings.Contains(err.Error(), "read tokenizer") {
		t.Fatalf("Load() error = %q, want it to name the read", err)
	}
}

// A legacy merges file ("a b" lines) is still accepted.
func TestLegacyMergeLines(t *testing.T) {
	tok, err := New(minimalSpec(func(d map[string]any) {
		model(d)["merges"] = []any{"a b"}
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ids, err := tok.Encode("ab")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("Encode(\"ab\") = %v, want the merged pair as one id", ids)
	}
}

func TestEncodeRejectsInvalidUTF8(t *testing.T) {
	tok, err := New(minimalSpec(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tok.Encode("a\xffb"); err == nil {
		t.Fatal("Encode() error = nil, want a UTF-8 error")
	}
}
