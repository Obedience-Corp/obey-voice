package hfbpe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// referenceTokenizer locates the Audio8 checkpoint's own tokenizer.json. It is
// 5.8 MB of vendor data and is not committed, so this test skips without it;
// TestMiniTokenizerParity is the always-on gate and this is the full-vocabulary
// one. Same locator convention as the other Audio8 tests
// (scripts/audio8_bench/README.md).
func referenceTokenizer(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("AUDIO8_TOKENIZER_JSON"); p != "" {
		return p
	}
	if dir := os.Getenv("AUDIO8_MODEL_DIR"); dir != "" {
		p := filepath.Join(dir, "tokenizer", "tokenizer.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skipf("no tokenizer/tokenizer.json under AUDIO8_MODEL_DIR=%s", dir)
	}
	t.Skip("set AUDIO8_MODEL_DIR (or AUDIO8_TOKENIZER_JSON) to run the full-vocabulary parity corpus")
	return ""
}

// TestAudio8ParityCorpus is the real gate: every case in audio8_parity.json was
// encoded by the reference HuggingFace tokenizer reading this exact file, and
// the Go encoder has to reproduce all of it byte for byte.
func TestAudio8ParityCorpus(t *testing.T) {
	path := referenceTokenizer(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "audio8_parity.json"))
	if err != nil {
		t.Fatalf("read audio8_parity.json: %v", err)
	}
	var doc struct {
		TokenizerSHA256 string `json:"tokenizer_sha256"`
		Cases           []struct {
			Text string  `json:"text"`
			IDs  []int64 `json:"ids"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse audio8_parity.json: %v", err)
	}
	if len(doc.Cases) < 200 {
		t.Fatalf("corpus has %d cases, want at least 200", len(doc.Cases))
	}

	// The corpus is only evidence about the tokenizer it was generated from.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(onDisk)
	if got := hex.EncodeToString(sum[:]); got != doc.TokenizerSHA256 {
		t.Skipf("%s is sha256 %s, corpus was generated from %s - regenerate with scripts/audio8_tokenizer/gen_parity.py",
			path, got, doc.TokenizerSHA256)
	}

	tok, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	var failed int
	for _, tc := range doc.Cases {
		got, err := tok.Encode(tc.Text)
		if err != nil {
			t.Errorf("Encode(%q) error = %v", tc.Text, err)
			failed++
			continue
		}
		if !slices.Equal(got, tc.IDs) {
			t.Errorf("Encode(%q)\n got %v\nwant %v", tc.Text, got, tc.IDs)
			failed++
		}
	}
	t.Logf("%d/%d parity cases matched exactly", len(doc.Cases)-failed, len(doc.Cases))
}

// TestAudio8FixtureRoundTrips proves the integration test's captured ids and
// this encoder are the same tokenizer: the fixture the DualAR pipeline replays
// must come back identical from the real thing.
func TestAudio8FixtureRoundTrips(t *testing.T) {
	path := referenceTokenizer(t)
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "audio8_tokenizer_fixture.json"))
	if err != nil {
		t.Fatalf("read the integration fixture: %v", err)
	}
	var doc struct {
		Encodings map[string][]int64 `json:"encodings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the integration fixture: %v", err)
	}
	if len(doc.Encodings) == 0 {
		t.Fatal("the integration fixture is empty")
	}
	tok, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	for text, want := range doc.Encodings {
		got, err := tok.Encode(text)
		if err != nil {
			t.Errorf("Encode(%q) error = %v", text, err)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("Encode(%q)\n got %v\nwant %v (captured)", text, got, want)
		}
	}
	t.Logf("%d captured fixture encodings reproduced", len(doc.Encodings))
}

func BenchmarkEncodeSentence(b *testing.B) {
	path := os.Getenv("AUDIO8_TOKENIZER_JSON")
	if path == "" {
		if dir := os.Getenv("AUDIO8_MODEL_DIR"); dir != "" {
			path = filepath.Join(dir, "tokenizer", "tokenizer.json")
		}
	}
	if path == "" {
		b.Skip("set AUDIO8_MODEL_DIR to benchmark against the real vocabulary")
	}
	tok, err := Load(path)
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	const text = "It continues here. Then a third thought arrives, and the sentence keeps going."
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tok.Encode(text); err != nil {
			b.Fatal(err)
		}
	}
}
