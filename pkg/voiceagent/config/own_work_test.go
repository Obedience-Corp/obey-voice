package config

import (
	"strings"
	"testing"
)

func TestWorkDefaultsToOwnOffOllamaAndWorkerOnIt(t *testing.T) {
	for provider, want := range map[string]string{
		"": WorkWorker, "ollama": WorkWorker, " Ollama ": WorkWorker,
		"claude": WorkOwn, "codex": WorkOwn, "cursor": WorkOwn, "grok": WorkOwn, "muse-spark": WorkOwn, "obey": WorkOwn,
	} {
		if got := WorkOf(&Config{BrainProvider: provider}); got != want {
			t.Errorf("WorkOf(%q) = %q, want %q", provider, got, want)
		}
	}
	if got := WorkOf(&Config{BrainProvider: "claude", PersonaWork: "Worker"}); got != WorkWorker {
		t.Errorf("a persona set to worker on claude = %q", got)
	}
	if got := WorkOf(&Config{BrainProvider: "ollama", PersonaWork: WorkOwn}); got != WorkOwn {
		t.Errorf("a persona set to own on ollama = %q", got)
	}
	if got := WorkOf(&Config{BrainProvider: "claude", PersonaNamesWorker: true, VoiceWorkerProvider: "ollama"}); got != WorkWorker {
		t.Errorf("a persona on claude that names its own worker = %q, want worker", got)
	}
	if got := WorkOf(&Config{BrainProvider: "claude", PersonaNamesWorker: true, PersonaWork: WorkOwn}); got != WorkOwn {
		t.Errorf("a persona that names a worker but is set to own = %q", got)
	}
	if got := WorkOf(&Config{BrainProvider: "claude", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "qwen3:8b"}); got != WorkOwn {
		t.Errorf("a worker selected only in Settings changed the default to %q", got)
	}
	for _, bad := range []string{"decide", "self", "both"} {
		if err := ValidateWork(bad); err == nil {
			t.Errorf("ValidateWork(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"", "own", "worker", " OWN "} {
		if err := ValidateWork(ok); err != nil {
			t.Errorf("ValidateWork(%q) = %v", ok, err)
		}
	}
}

func TestOwnWorkConfigRunsOnThePersonasBackend(t *testing.T) {
	tiers := DefaultVoiceWorkers()
	tiers.Mode = WorkerModeTiered
	cfg := &Config{BrainProvider: "claude", ClaudeModel: "haiku", OllamaModel: "live:8b", OllamaNumCtx: 8192, ActivePersona: "mira", WorkDir: "/work/mira",
		VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker:8b", VoiceWorkerOllamaHost: "http://worker:11434", VoiceWorkerNumCtx: 65536,
		VoiceWorkers: tiers, DelegationEnabled: true}
	own := OwnWorkConfig(cfg)
	if own.VoiceWorkerProvider != "claude" || own.VoiceWorkerModel != "haiku" || own.VoiceWorkerOllamaHost != "" || own.VoiceWorkerNumCtx != 8192 ||
		own.VoiceWorkers == nil || own.VoiceWorkers.Mode != WorkerModeLegacy || own.VoiceWorkers.Policy != tiers.Policy || own.VoiceToolsEnabled || own.DelegationEnabled || own.ActivePersona != "mira" || own.WorkDir != "/work/mira" {
		t.Fatalf("own work config = %+v", own)
	}
	cfg.VoiceToolsEnabled = true
	if !OwnWorkConfig(cfg).VoiceToolsEnabled {
		t.Fatal("own work config dropped voice_tools_enabled")
	}
	if cfg.VoiceWorkerProvider != "ollama" || cfg.VoiceWorkers.Mode != WorkerModeTiered || !cfg.DelegationEnabled {
		t.Fatal("OwnWorkConfig changed the bound config")
	}
	worker := WorkerBrainConfig(own)
	if !worker.IsWorker || worker.BrainProvider != "claude" || worker.ClaudeModel != "haiku" {
		t.Fatalf("worker brain config = %s/%s worker=%v", worker.BrainProvider, worker.ClaudeModel, worker.IsWorker)
	}

	local := OwnWorkConfig(&Config{OllamaModel: "live:8b", OllamaHost: "http://persona:11434", OllamaNumCtx: 8192, VoiceWorkerOllamaHost: "http://worker:11434", VoiceWorkerNumCtx: 65536})
	worker = WorkerBrainConfig(local)
	if worker.BrainProvider != "ollama" || worker.OllamaModel != "live:8b" || worker.OllamaHost != "http://persona:11434" || worker.OllamaNumCtx != 8192 || local.VoiceToolsEnabled {
		t.Fatalf("own Ollama worker = %s %s at %s num_ctx %d tools %v", worker.BrainProvider, worker.OllamaModel, worker.OllamaHost, worker.OllamaNumCtx, local.VoiceToolsEnabled)
	}
}

func TestOwnWorkTierIsTheRoutedTiersLimitsAndPolicy(t *testing.T) {
	if tier, err := OwnWorkTier(&Config{VoiceWorkers: DefaultVoiceWorkers()}, WorkerTierSimple); tier != nil || err != nil {
		t.Fatalf("legacy mode = %+v, %v", tier, err)
	}
	workers := DefaultVoiceWorkers()
	workers.Mode = WorkerModeTiered
	workers.Tiers.Medium.Limits.RuntimeSeconds = 300
	runtime := 30
	cfg := &Config{VoiceWorkers: workers, PersonaWorkerTiers: &WorkerTierOverrides{Simple: &WorkerTierOverride{Limits: &WorkerLimits{RuntimeSeconds: runtime, ModelRequests: 2, ToolCalls: 3, TransportRetries: 0}}}}
	tier, err := OwnWorkTier(cfg, WorkerTierSimple)
	if err != nil || tier == nil || tier.Tier != WorkerTierSimple || tier.Limits.RuntimeSeconds != 30 || tier.Limits.ModelRequests != 2 || tier.Limits.ToolCalls != 3 || tier.Strict {
		t.Fatalf("simple = %+v, %v; want the persona's override", tier, err)
	}
	if tier, err = OwnWorkTier(cfg, WorkerTierMedium); err != nil || tier.Limits.RuntimeSeconds != 300 {
		t.Fatalf("an unassigned tier = %+v, %v; want its limits", tier, err)
	}
	if OwnWorkStrict(cfg) {
		t.Fatal("advisory reported strict")
	}
	daily := int64(1)
	workers.Policy.ChargePolicy, workers.Policy.DailyChargeMicroUSD = "strict", &daily
	if tier, err = OwnWorkTier(cfg, WorkerTierComplex); err != nil || !tier.Strict || !OwnWorkStrict(cfg) {
		t.Fatalf("strict = %+v, %v", tier, err)
	}
	if _, err = OwnWorkTier(cfg, ""); err == nil || !strings.Contains(err.Error(), "no known tier") {
		t.Fatalf("no tier = %v", err)
	}
}
