package config

import (
	"path/filepath"
	"testing"
)

func TestCodexEnvironmentAndWorkerModel(t *testing.T) {
	original := configFile
	configFile = filepath.Join(t.TempDir(), "config.yaml")
	defer func() { configFile = original }()
	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "codex")
	t.Setenv("CODEX_MODEL", "voice-model")
	t.Setenv("CODEX_REASONING_EFFORT", "low")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrainProvider != "codex" || cfg.CodexModel != "voice-model" || cfg.CodexReasoningEffort != "low" {
		t.Fatal("Codex env overrides missing")
	}
	cfg.VoiceWorkerProvider = "codex"
	cfg.VoiceWorkerModel = "worker-model"
	worker := WorkerBrainConfig(cfg)
	if BrainModelFor(worker, "codex") != "worker-model" || cfg.CodexModel != "voice-model" {
		t.Fatal("worker mutated voice model")
	}
	spec, ok := SpecFor("codex_reasoning_effort")
	if !ok || spec.Type != TypeEnum || !spec.AllowsEmpty {
		t.Fatal("effort schema incomplete")
	}
}
