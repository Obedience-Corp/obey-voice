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
		SetBrainModel(&w, w.BrainProvider, model)
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
