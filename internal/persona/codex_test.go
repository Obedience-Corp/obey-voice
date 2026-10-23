package persona

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"slices"
	"testing"
)

func TestCodexPersonaAndWorkerModelRouting(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", CodexReasoningEffort: "low"}
	p := &Profile{ID: "codex-helper", Brain: Brain{Provider: "codex", Model: "voice-model", Worker: Worker{Provider: "codex", Model: "worker-model"}}}
	Apply(cfg, p)
	if cfg.BrainProvider != "codex" || cfg.CodexModel != "voice-model" {
		t.Fatal("persona model missing")
	}
	if !slices.Contains(OverriddenKeys(cfg, p), "codex_model") {
		t.Fatal("persona override badge missing")
	}
	worker := config.WorkerBrainConfig(cfg)
	if worker.BrainProvider != "codex" || worker.CodexModel != "worker-model" || worker.CodexReasoningEffort != "low" {
		t.Fatalf("worker routing: %+v", worker)
	}
}
