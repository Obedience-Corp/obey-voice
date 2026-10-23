package tui

import (
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"testing"
)

func TestCodexSettingsModelAndEffortPersistence(t *testing.T) {
	cfg := &config.Config{BrainProvider: "codex", CodexModel: "fixture-model"}
	m := settingsModel{cfg: cfg, modelItems: []string{"default", "fixture-model"}}
	if m.activeModel() != "fixture-model" || m.modelRows()[0].key != "codex_model" {
		t.Fatal("Codex model not routed")
	}
	var savedKey string
	var savedValue any
	m.saveConfig = func(k string, v any) error { savedKey = k; savedValue = v; return nil }
	rows := m.codexRows()
	if len(rows) != 1 {
		t.Fatal("missing effort row")
	}
	rows[0].set(&m, 3) // low
	if savedKey != "codex_reasoning_effort" || savedValue != "low" || cfg.CodexReasoningEffort != "low" {
		t.Fatalf("save failed: %s %v", savedKey, savedValue)
	}
	m.saveConfig = func(string, any) error { return errors.New("disk full") }
	rows[0].set(&m, 4)
	if cfg.CodexReasoningEffort != "low" {
		t.Fatal("failed save changed effective setting")
	}
	cfg.BrainProvider = "ollama"
	if len(m.codexRows()) != 0 {
		t.Fatal("Codex setting leaked into other provider")
	}
}
