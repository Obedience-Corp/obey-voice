package tui

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// claudeSettings builds the Settings model from what discovery reports for
// Claude, with no claude CLI on PATH.
func claudeSettings(t *testing.T, cfg *config.Config) settingsModel {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	m := settingsModel{cfg: cfg, providers: []discovery.ProviderInfo{discovery.DiscoverProvider(t.Context(), cfg, "claude")}}
	m.buildModelItems()
	return m
}

func TestClaudeSettingsModelRowSavesClaudeModel(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "claude", ClaudeModel: "haiku"}
	m := claudeSettings(t, cfg)
	row := m.modelRows()[0]
	if row.key != "claude_model" || row.detail != "" || row.value != "haiku" || len(row.options) != 2 || row.options[row.current] != "haiku" {
		t.Fatalf("Claude model row = %+v", row)
	}
	row.set(&m, 0)
	saved, err := config.LoadRaw()
	if err != nil || cfg.ClaudeModel != "" || saved.ClaudeModel != "" || m.activeModel() != "default" {
		t.Fatalf("choosing default left model %q, saved %q, err = %v", cfg.ClaudeModel, saved.ClaudeModel, err)
	}
	row.set(&m, 1)
	saved, err = config.LoadRaw()
	if err != nil || cfg.ClaudeModel != "haiku" || saved.ClaudeModel != "haiku" || m.activeModel() != "haiku" {
		t.Fatalf("model = %q, saved = %q, err = %v", cfg.ClaudeModel, saved.ClaudeModel, err)
	}
}

func TestClaudeSettingsListsDefaultOnce(t *testing.T) {
	for _, model := range []string{"", "default", "Default"} {
		m := claudeSettings(t, &config.Config{BrainProvider: "claude", ClaudeModel: model})
		if row := m.modelRows()[0]; len(row.options) != 1 || row.options[0] != "default" {
			t.Fatalf("claude_model %q: options = %v", model, row.options)
		}
	}
}
