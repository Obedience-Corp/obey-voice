package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
