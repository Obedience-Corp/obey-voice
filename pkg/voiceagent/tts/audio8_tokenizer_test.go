package tts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestAudio8TokenizerPath(t *testing.T) {
	got := audio8TokenizerPath(filepath.Join("models", "audio8-tts"))
	want := filepath.Join("models", "audio8-tts", "tokenizer", "tokenizer.json")
	if got != want {
		t.Fatalf("audio8TokenizerPath() = %q, want %q (the vendor export's layout)", got, want)
	}
}

func TestNewAudio8TokenizerReportsAMissingFile(t *testing.T) {
	_, err := NewAudio8Tokenizer(t.TempDir())
	if err == nil {
		t.Fatal("NewAudio8Tokenizer() error = nil, want a read error")
	}
	if !strings.Contains(err.Error(), "audio8 tokenizer") || !strings.Contains(err.Error(), "tokenizer.json") {
		t.Fatalf("NewAudio8Tokenizer() error = %q, want it to name the provider and the file", err)
	}
}

func TestNewAudio8TokenizerReportsAMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio8TokenizerPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewAudio8Tokenizer(dir)
	if err == nil {
		t.Fatal("NewAudio8Tokenizer() error = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), "parse tokenizer.json") {
		t.Fatalf("NewAudio8Tokenizer() error = %q, want it to name the parse", err)
	}
}

func TestAudio8Installed(t *testing.T) {
	if Audio8Installed(nil) {
		t.Error("Audio8Installed(nil) = true, want false")
	}
	modelsDir := t.TempDir()
	cfg := &config.Config{ModelsDir: modelsDir}
	if Audio8Installed(cfg) {
		t.Error("Audio8Installed() = true for an empty models dir, want false")
	}
	dir := audio8ModelDir(cfg)
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio8TokenizerPath(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Audio8Installed(cfg) {
		t.Error("Audio8Installed() = true with no runtime_manifest.json, want false")
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime_manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Audio8Installed(cfg) {
		t.Error("Audio8Installed() = false with both files present, want true")
	}
}

// installedAudio8Dir finds an Audio8 export to test against: the configured
// install first, then the snapshot the bench harness uses. Skips otherwise -
// the export is a download, not a repo artifact.
func installedAudio8Dir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		audio8ModelDir(&config.Config{ModelsDir: config.DefaultModelsDir()}),
		os.Getenv("AUDIO8_MODEL_DIR"),
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(audio8TokenizerPath(dir)); err == nil {
			return dir
		}
	}
	t.Skip("no Audio8 export installed (set AUDIO8_MODEL_DIR to point at one)")
	return ""
}

// TestAudio8TokenizerReproducesTheIntegrationFixture is the seam-level check:
// the captured ids the integration test replays must be exactly what the real
// tokenizer returns through the Audio8Tokenizer interface, so wiring it in
// changes nothing about how that test drives the DualAR pipeline.
func TestAudio8TokenizerReproducesTheIntegrationFixture(t *testing.T) {
	dir := installedAudio8Dir(t)
	tok, err := NewAudio8Tokenizer(dir)
	if err != nil {
		t.Fatalf("NewAudio8Tokenizer(%s): %v", dir, err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "audio8_tokenizer_fixture.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Encodings map[string][]int64 `json:"encodings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(doc.Encodings) == 0 {
		t.Fatal("fixture is empty")
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
}
