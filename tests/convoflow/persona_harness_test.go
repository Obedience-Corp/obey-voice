package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestLoadFixtureConfigPlaybackVolume(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want float64
	}{
		{"omitted", "brain_provider: ollama\n", config.DefaultPlaybackVolume},
		{"explicit_mute", "playback_volume: 0\n", 0},
		{"fractional", "playback_volume: 0.37\n", 0.37},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadFixtureConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PlaybackVolume != tc.want {
				t.Fatalf("playback volume=%v want=%v", cfg.PlaybackVolume, tc.want)
			}
		})
	}
}

func TestLoadFixtureConfigUsesExplicitFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nollama_model: fixture-model\nactive_persona: samantha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFixtureConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxHistory != 40 {
		t.Fatalf("fixture max_history = %d; zero erases context after each turn", cfg.MaxHistory)
	}
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "fixture-model" || cfg.ActivePersona != "samantha" {
		t.Fatalf("fixture config = %+v", cfg)
	}
}

func TestPersonaProofScenarioCoversControlAndFailedSelection(t *testing.T) {
	scn, err := loadScenario(filepath.Join("scenarios", "persona-proof.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var ui, failed bool
	for _, turn := range scn.Turns {
		ui = ui || turn.Op == "select_persona"
		failed = failed || turn.ExpectError != ""
	}
	if !ui || !failed {
		t.Fatalf("persona proof controls: ui=%t failed=%t", ui, failed)
	}
}
