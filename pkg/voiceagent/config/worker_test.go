package config

import "testing"

func TestVoiceWorkerProviderDefaultsGrok(t *testing.T) {
	if got := VoiceWorkerProviderOf(&Config{}); got != "grok" {
		t.Fatalf("default worker = %q, want grok", got)
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
