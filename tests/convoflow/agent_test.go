package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestResolveConfigForcesTheGateKnobs(t *testing.T) {
	base := &config.Config{
		BrainProvider:       "claude",
		OllamaModel:         "gemma3",
		OllamaHost:          "http://cfg:11434",
		VoiceWorkerProvider: "grok",
		VoiceWorkerModel:    "pinned-in-config",
		VoiceToolsEnabled:   false,
		VoiceRouterTools:    "legacy",
		TTSProvider:         "qwen",
	}
	scn, err := loadScenarioString("t", "name: t\nrouter_tools: route_only\ntts: { provider: kokoro, voice: af_heart }\n")
	if err != nil {
		t.Fatal(err)
	}
	k := knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "ollama", ollamaHost: "http://localhost:11434"}
	runDir := t.TempDir()

	cfg, prof, err := resolveConfig(base, scn, k, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if base.VoiceToolsEnabled || base.OllamaModel != "gemma3" {
		t.Fatal("resolveConfig must not mutate the operator's config")
	}
	if !cfg.VoiceToolsEnabled {
		t.Fatal("voice_tools_enabled must be forced on")
	}
	if cfg.VoiceRouterTools != "route_only" {
		t.Fatalf("voice_router_tools = %q, want route_only", cfg.VoiceRouterTools)
	}
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "qwen3:8b" || cfg.OllamaHost != "http://localhost:11434" {
		t.Fatalf("live knobs did not win: %s %s %s", cfg.BrainProvider, cfg.OllamaModel, cfg.OllamaHost)
	}
	if cfg.VoiceWorkerProvider != "ollama" {
		t.Fatalf("worker provider = %q", cfg.VoiceWorkerProvider)
	}
	if cfg.VoiceWorkerModel != "" {
		t.Fatalf("empty worker knob must inherit the live model, got %q", cfg.VoiceWorkerModel)
	}
	if cfg.WorkDir != filepath.Join(runDir, "work") {
		t.Fatalf("work_dir = %q", cfg.WorkDir)
	}
	if st, err := os.Stat(cfg.WorkDir); err != nil || !st.IsDir() {
		t.Fatalf("work dir must exist before providers are built: %v", err)
	}
	if cfg.TTSProvider != "kokoro" || cfg.TTSVoice != "af_heart" {
		t.Fatalf("scenario tts did not apply: %s %s", cfg.TTSProvider, cfg.TTSVoice)
	}
	if prof.Profile != "local" || prof.WorkerModel != "qwen3:8b" || prof.WorkerHost != "http://localhost:11434" {
		t.Fatalf("profile = %+v", prof)
	}
	if prof.silent() {
		t.Fatal("kokoro is not silent")
	}
}

func TestResolveConfigScenarioLegacyAndYAMLFallback(t *testing.T) {
	base := &config.Config{OllamaModel: "gemma3"}
	scn, err := loadScenarioString("t", "name: t\nrouter_tools: legacy\nbrain: { model: qwen3:30b }\nworker: { model: qwen3:8b }\ntts: { provider: none }\n")
	if err != nil {
		t.Fatal(err)
	}
	k := knobs{liveProvider: "ollama", workerProvider: "ollama", ollamaHost: "http://localhost:11434"}
	cfg, prof, err := resolveConfig(base, scn, k, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoiceRouterTools != "legacy" {
		t.Fatalf("voice_router_tools = %q, want legacy", cfg.VoiceRouterTools)
	}
	if cfg.OllamaModel != "qwen3:30b" {
		t.Fatalf("YAML model must apply when the knob is empty, got %q", cfg.OllamaModel)
	}
	if cfg.VoiceWorkerModel != "qwen3:8b" || prof.WorkerModel != "qwen3:8b" {
		t.Fatalf("YAML worker model did not apply: %q / %q", cfg.VoiceWorkerModel, prof.WorkerModel)
	}
	if !prof.silent() {
		t.Fatal("tts none must be silent")
	}
}

func TestResolveConfigStubWorkerLeavesConfigProviderAlone(t *testing.T) {
	base := &config.Config{VoiceWorkerProvider: "grok"}
	scn, _ := loadScenarioString("t", "name: t\n")
	k := knobs{liveProvider: "ollama", liveModel: "qwen3:8b", workerProvider: "stub", ollamaHost: "http://localhost:11434"}
	cfg, prof, err := resolveConfig(base, scn, k, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoiceWorkerProvider != "grok" {
		t.Fatalf("stub must never be written into config, got %q", cfg.VoiceWorkerProvider)
	}
	if prof.Profile != "stub" || prof.WorkerProvider != "stub" {
		t.Fatalf("profile = %+v", prof)
	}
}

func TestResolveConfigSkipsWhenToolsForcedOffByEnv(t *testing.T) {
	t.Setenv("VOICE_TOOLS_ENABLED", "false")
	scn, _ := loadScenarioString("t", "name: t\n")
	_, _, err := resolveConfig(&config.Config{}, scn, knobs{liveModel: "qwen3:8b"}, t.TempDir())
	var skip skipError
	if !errors.As(err, &skip) {
		t.Fatalf("err = %v, want skipError", err)
	}
}

func TestWriteResolvedConfigCarriesKnobsOnly(t *testing.T) {
	dir := t.TempDir()
	if err := writeResolvedConfig(dir, runProfile{Profile: "local", LiveModel: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.resolved.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got == "" || !contains(got, "live_model: qwen3:8b") {
		t.Fatalf("config.resolved.yaml = %q", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
