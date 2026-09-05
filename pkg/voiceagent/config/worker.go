package config

import "strings"

// WorkerProviders are the backends allowed for voice_worker_provider.
var WorkerProviders = []string{"ollama", "grok", "claude"}

// VoiceWorkerProviderOf returns the configured worker brain, defaulting to grok.
func VoiceWorkerProviderOf(cfg *Config) string {
	if cfg == nil {
		return "grok"
	}
	name := strings.TrimSpace(strings.ToLower(cfg.VoiceWorkerProvider))
	if name == "" {
		return "grok"
	}
	return name
}

// WorkerBrainConfig clones cfg for a second brain.Provider worker instance.
func WorkerBrainConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	w := *cfg
	w.BrainProvider = VoiceWorkerProviderOf(cfg)
	if model := strings.TrimSpace(cfg.VoiceWorkerModel); model != "" {
		switch w.BrainProvider {
		case "ollama":
			w.OllamaModel = model
		case "grok":
			w.GrokModel = model
		}
	}
	if host := strings.TrimSpace(cfg.VoiceWorkerOllamaHost); host != "" && w.BrainProvider == "ollama" {
		w.OllamaHost = host
	}
	return &w
}

// TwoLocalOllamaWorkers reports when the live brain and worker are both local
// Ollama on the same host — doctor warns but does not refuse (D5).
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
