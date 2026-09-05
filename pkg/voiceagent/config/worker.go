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
