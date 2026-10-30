package tui

import (
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"testing"
)

func TestOllamaThinkingSaveFailurePreservesSetting(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", OllamaThink: "false"}
	m := settingsModel{cfg: cfg, saveConfig: func(string, any) error { return errors.New("disk full") }}
	row := m.ollamaThinkingRows()[0]
	row.set(&m, indexOf(row.options, "true"))
	if cfg.OllamaThink != "false" {
		t.Fatal("failed save changed thinking")
	}
	cfg.BrainProvider = "codex"
	if len(m.ollamaThinkingRows()) != 0 {
		t.Fatal("Ollama setting shown for Codex")
	}
}
