package config

import "testing"

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
		own.VoiceWorkers == nil || own.VoiceWorkers.Mode != WorkerModeLegacy || own.VoiceWorkers.Policy != tiers.Policy || !own.VoiceToolsEnabled || own.DelegationEnabled || own.ActivePersona != "mira" || own.WorkDir != "/work/mira" {
		t.Fatalf("own work config = %+v", own)
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
