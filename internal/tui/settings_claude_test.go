package tui

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestClaudeSettingsModelRowSavesClaudeModel(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "claude"}
	m := settingsModel{cfg: cfg, modelItems: []string{"default", "haiku"}}
	row := m.modelRows()[0]
	if row.key != "claude_model" || row.detail != "" {
		t.Fatalf("Claude model row = %+v", row)
	}
	row.set(&m, 1)
	saved, err := config.LoadRaw()
	if err != nil || cfg.ClaudeModel != "haiku" || saved.ClaudeModel != "haiku" || m.activeModel() != "haiku" {
		t.Fatalf("model = %q, saved = %+v, err = %v", cfg.ClaudeModel, saved, err)
	}
	m.modelRows()[0].set(&m, 0)
	saved, err = config.LoadRaw()
	if err != nil || cfg.ClaudeModel != "" || saved.ClaudeModel != "" || m.activeModel() != "default" {
		t.Fatalf("choosing default left model %q, saved %q, err = %v", cfg.ClaudeModel, saved.ClaudeModel, err)
	}
}
