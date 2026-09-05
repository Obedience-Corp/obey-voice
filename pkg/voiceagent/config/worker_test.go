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
