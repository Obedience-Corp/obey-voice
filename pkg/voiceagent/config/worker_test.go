package config

import (
	"os"
	"path/filepath"
	"testing"
)

// An unselected worker must never reach for a remote CLI on its own. It
// inherits the live brain, and says so; the grok default it used to carry is
// how a spawn ended up on a model the operator never chose.
func TestUnselectedWorkerInheritsTheLiveBrain(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"no config at all", nil, WorkerFallbackProvider},
		{"empty config", &Config{}, WorkerFallbackProvider},
		{"inherits ollama", &Config{BrainProvider: "ollama"}, "ollama"},
		{"inherits claude", &Config{BrainProvider: "claude"}, "claude"},
		{"cursor cannot work, so it falls back", &Config{BrainProvider: "cursor"}, WorkerFallbackProvider},
		{"an explicit choice wins", &Config{BrainProvider: "ollama", VoiceWorkerProvider: "grok"}, "grok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VoiceWorkerProviderOf(tt.cfg); got != tt.want {
				t.Fatalf("worker = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkerSelectedReportsTheOperatorsChoice(t *testing.T) {
	if WorkerSelected(nil) || WorkerSelected(&Config{}) || WorkerSelected(&Config{BrainProvider: "ollama"}) {
		t.Fatal("an unset voice_worker_provider must not read as selected")
	}
	if !WorkerSelected(&Config{VoiceWorkerProvider: "grok"}) {
		t.Fatal("an explicit worker must read as selected")
	}
}

func TestTwoLocalOllamaWorkers(t *testing.T) {
	cfg := &Config{
		BrainProvider:       "ollama",
		VoiceWorkerProvider: "ollama",
		OllamaHost:          "http://localhost:11434",
	}
	if !TwoLocalOllamaWorkers(cfg) {
		t.Fatal("expected two-local warning case")
	}
	cfg.VoiceWorkerOllamaHost = "http://remote:11434"
	if TwoLocalOllamaWorkers(cfg) {
		t.Fatal("different worker host should not warn as same-host")
	}
}

func TestDistinctLocalOllamaModels(t *testing.T) {
	same := &Config{
		BrainProvider:       "ollama",
		VoiceWorkerProvider: "ollama",
		OllamaHost:          "http://localhost:11434",
		OllamaModel:         "qwen3:8b",
	}
	if DistinctLocalOllamaModels(same) {
		t.Fatal("same model must not be distinct")
	}
	same.VoiceWorkerModel = ""
	if DistinctLocalOllamaModels(same) {
		t.Fatal("empty worker model inherits live and is not distinct")
	}
	same.VoiceWorkerModel = "qwen3:8b:latest"
	if DistinctLocalOllamaModels(same) {
		t.Fatal(":latest suffix is the same model")
	}
	same.VoiceWorkerModel = "qwen3:30b"
	if !DistinctLocalOllamaModels(same) {
		t.Fatal("different worker model on the same host is distinct")
	}
	same.VoiceWorkerOllamaHost = "http://studio:11434"
	if DistinctLocalOllamaModels(same) {
		t.Fatal("different host is not a local distinct pair")
	}
}

func TestWorkerBrainConfigGrokModelPin(t *testing.T) {
	cfg := &Config{
		VoiceWorkerProvider: "grok",
		VoiceWorkerModel:    "grok-3",
	}
	got := WorkerBrainConfig(cfg)
	if got.GrokModel != "grok-3" {
		t.Fatalf("GrokModel = %q, want grok-3", got.GrokModel)
	}
}

func TestWorkerBrainConfigMarksTheWorker(t *testing.T) {
	cfg := &Config{BrainProvider: "ollama"}
	if !WorkerBrainConfig(cfg).IsWorker {
		t.Fatal("the worker config must say it is a worker")
	}
	if cfg.IsWorker {
		t.Fatal("the live config must not")
	}
}

// The boundary is the campaign root when the work directory sits in one,
// the work directory itself otherwise, and nothing when switched off.
func TestWorkerBrainConfigFencesTheWorkerToItsCampaign(t *testing.T) {
	root := writeCampaignMarker(t, t.TempDir(), "name: demo\nid: c1\n")
	inside := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{BrainProvider: "ollama", WorkDir: inside, VoiceWorkerBoundary: true}
	if got := WorkerBrainConfig(cfg).WorkerBoundary; got != root {
		t.Fatalf("boundary inside a campaign = %q, want %q", got, root)
	}

	plain := t.TempDir()
	cfg = &Config{BrainProvider: "ollama", WorkDir: plain, VoiceWorkerBoundary: true}
	if got := WorkerBrainConfig(cfg).WorkerBoundary; got != WorkDirFrom(cfg) {
		t.Fatalf("boundary outside a campaign = %q, want the work dir %q", got, WorkDirFrom(cfg))
	}

	cfg = &Config{BrainProvider: "ollama", WorkDir: inside}
	if got := WorkerBrainConfig(cfg).WorkerBoundary; got != "" {
		t.Fatalf("boundary with voice_worker_boundary off = %q, want none", got)
	}
	if cfg.WorkerBoundary != "" {
		t.Fatal("the live config must never carry a boundary")
	}
}
