package config

import (
	"fmt"
	"strings"
)

// Who runs a persona's background work.
const (
	// WorkOwn runs it on the persona's own backend and model, in a session
	// of its own.
	WorkOwn = "own"
	// WorkWorker hands it to the configured worker.
	WorkWorker = "worker"
)

// WorkChoices are the values brain.work accepts; empty takes the default.
var WorkChoices = []string{WorkOwn, WorkWorker}

// ValidateWork accepts an empty value or one of WorkChoices.
func ValidateWork(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", WorkOwn, WorkWorker:
		return nil
	}
	return fmt.Errorf("brain.work %q: use %s or %s (empty takes the default)", value, WorkOwn, WorkWorker)
}

// DefaultWork is who does the work of a persona on provider when its
// profile does not say: the persona itself on any backend but Ollama, and
// the configured worker on Ollama.
func DefaultWork(provider string) string {
	if brainProviderName(provider) == "ollama" {
		return WorkWorker
	}
	return WorkOwn
}

// WorkOf is who runs the bound persona's background work.
func WorkOf(cfg *Config) string {
	if cfg == nil {
		return WorkWorker
	}
	if value := strings.ToLower(strings.TrimSpace(cfg.PersonaWork)); value == WorkOwn || value == WorkWorker {
		return value
	}
	return DefaultWork(cfg.BrainProvider)
}

// OwnWorkConfig is the job configuration for work the persona does itself:
// cfg with the persona's own backend, model and host in place of the
// configured worker. Worker tiers apply only to the configured worker, so an
// own job runs outside them, with the tools a tiered worker would get.
func OwnWorkConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	own := cfg.Clone()
	provider := brainProviderName(cfg.BrainProvider)
	own.BrainProvider = provider
	own.VoiceWorkerProvider = provider
	own.VoiceWorkerModel = BrainModelFor(cfg, provider)
	own.VoiceWorkerOllamaHost = ""
	// Same model, same window: a different num_ctx would make Ollama reload
	// the model the persona is talking on.
	own.VoiceWorkerNumCtx = cfg.OllamaNumCtx
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == WorkerModeTiered {
		own.VoiceToolsEnabled = true
	}
	own.VoiceWorkers = nil
	own.DelegationEnabled = false
	return own
}

func brainProviderName(provider string) string {
	if name := strings.ToLower(strings.TrimSpace(provider)); name != "" {
		return name
	}
	return "ollama"
}
