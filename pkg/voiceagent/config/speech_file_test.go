package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSpeechFileIsolatedFromEnvironmentAndHooks(t *testing.T) {
	t.Setenv("STT_PROVIDER", "whispercpp")
	t.Setenv("MODELS_DIR", "/not-this-model-dir")
	path := filepath.Join(t.TempDir(), "speech.json")
	dir := t.TempDir()
	doc := map[string]any{"schema": "obey-voice.speech-config/v1", "provider": "parakeet", "model": "tdt-0.6b-v2", "models_dir": dir, "language": "en"}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSpeechFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.STTProvider != "parakeet" || cfg.ModelsDir != dir || cfg.AcousticNameEnabled {
		t.Fatalf("not isolated: %s %s", cfg.STTProvider, cfg.ModelsDir)
	}
	doc["models_dir"] = "relative"
	data, _ = json.Marshal(doc)
	_ = os.WriteFile(path, data, 0600)
	if _, err = LoadSpeechFile(path); err == nil {
		t.Fatal("accepted relative models path")
	}
}
