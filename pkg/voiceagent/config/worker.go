package config

import (
	"slices"
	"strings"
)

// WorkerProviders are the backends allowed for voice_worker_provider.
var WorkerProviders = []string{"ollama", "grok", "claude"}

// WorkerFallbackProvider is where an unselected worker lands. It is the
// last resort behind the live brain, not a default anyone is meant to reach:
// Settings and the persona form both require a choice (D1).
const WorkerFallbackProvider = "ollama"

// WorkerSelected reports whether the operator has actually chosen a worker.
// A voice turn that spawns work on a model nobody picked is the failure this
// answers — the launcher and Settings render "not set" from it and ask.
func WorkerSelected(cfg *Config) bool {
	return cfg != nil && strings.TrimSpace(cfg.VoiceWorkerProvider) != ""
}

// VoiceWorkerProviderOf returns the worker brain. An unselected worker
// inherits the live brain rather than reaching for a remote default: the
// live model is already loaded, and local-first is the product. A live brain
// that cannot be a worker (cursor) falls back to WorkerFallbackProvider.
func VoiceWorkerProviderOf(cfg *Config) string {
	if cfg == nil {
		return WorkerFallbackProvider
	}
	if name := strings.TrimSpace(strings.ToLower(cfg.VoiceWorkerProvider)); name != "" {
		return name
	}
	live := strings.TrimSpace(strings.ToLower(cfg.BrainProvider))
	if WorkerProviderAllowed(live) {
		return live
	}
	return WorkerFallbackProvider
}

// WorkerProviderAllowed reports whether name can run async jobs.
func WorkerProviderAllowed(name string) bool {
	return slices.Contains(WorkerProviders, strings.TrimSpace(strings.ToLower(name)))
}

// WorkerBrainConfig clones cfg for a second brain.Provider worker instance.
func WorkerBrainConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	w := *cfg
	w.IsWorker = true
	w.BrainProvider = VoiceWorkerProviderOf(cfg)
	if model := strings.TrimSpace(cfg.VoiceWorkerModel); model != "" {
		SetBrainModel(&w, w.BrainProvider, model)
	}
	if host := strings.TrimSpace(cfg.VoiceWorkerOllamaHost); host != "" && w.BrainProvider == "ollama" {
		w.OllamaHost = host
	}
	w.WorkerBoundary = WorkerBoundaryFor(cfg)
	return &w
}

// WorkerBoundaryFor is the directory a spawned worker must stay under: the
// campaign root the work directory sits in, or the work directory itself when
// there is none. Empty when voice_worker_boundary is off.
func WorkerBoundaryFor(cfg *Config) string {
	if cfg == nil || !cfg.VoiceWorkerBoundary {
		return ""
	}
	workDir := WorkDirFrom(cfg)
	if c := CampaignFor(workDir); c.Found() {
		return c.Root
	}
	return workDir
}

// TwoLocalOllamaWorkers reports when the live brain and worker are both local
// Ollama on the same host. Host matching is literal EqualFold on the URL
// string (localhost vs 127.0.0.1 are different). Doctor and NewWorkerProvider
// use DistinctLocalOllamaModels to decide whether that pair is one load or two.
func TwoLocalOllamaWorkers(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	live := strings.TrimSpace(strings.ToLower(cfg.BrainProvider))
	if live == "" {
		live = "ollama"
	}
	worker := VoiceWorkerProviderOf(cfg)
	if live != "ollama" || worker != "ollama" {
		return false
	}
	liveHost := strings.TrimSpace(cfg.OllamaHost)
	if liveHost == "" {
		liveHost = "http://localhost:11434"
	}
	workerHost := strings.TrimSpace(cfg.VoiceWorkerOllamaHost)
	if workerHost == "" {
		workerHost = liveHost
	}
	return strings.EqualFold(liveHost, workerHost)
}

// DistinctLocalOllamaModels reports a same-host Ollama live+worker pair that
// would load two weight sets. Empty voice_worker_model inherits ollama_model,
// so the local product (one model, two clients) returns false.
func DistinctLocalOllamaModels(cfg *Config) bool {
	if !TwoLocalOllamaWorkers(cfg) {
		return false
	}
	live := strings.TrimSpace(cfg.OllamaModel)
	worker := strings.TrimSpace(cfg.VoiceWorkerModel)
	if worker == "" {
		worker = live
	}
	return !ollamaModelEqual(live, worker)
}

func ollamaModelEqual(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		return strings.TrimSuffix(s, ":latest")
	}
	return norm(a) == norm(b)
}

// BrainModelFor returns the model configured for a brain provider. Claude has
// no app-level model key, so it reports "".
func BrainModelFor(cfg *Config, provider string) string {
	if cfg == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return strings.TrimSpace(cfg.OllamaModel)
	case "grok":
		return strings.TrimSpace(cfg.GrokModel)
	case "cursor":
		return strings.TrimSpace(cfg.CursorModel)
	}
	return ""
}

// SetBrainModel writes model into the provider's model field; providers
// without a model key are left alone.
func SetBrainModel(cfg *Config, provider, model string) {
	if cfg == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		cfg.OllamaModel = model
	case "grok":
		cfg.GrokModel = model
	case "cursor":
		cfg.CursorModel = model
	}
}
