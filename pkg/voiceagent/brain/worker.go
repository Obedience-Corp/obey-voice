package brain

import (
	"fmt"
	"log"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// NewWorkerProvider constructs the async job worker as a second Provider
// instance from voice_worker_provider (default grok). Same-daemon Ollama
// live+worker is allowed: empty voice_worker_model reuses the live model
// (one load, two clients). Distinct models on the same host warn — they
// can evict the talking session — but still construct.
func NewWorkerProvider(cfg *config.Config) (Provider, error) {
	if config.DistinctLocalOllamaModels(cfg) {
		live := strings.TrimSpace(cfg.OllamaModel)
		worker := strings.TrimSpace(cfg.VoiceWorkerModel)
		log.Printf("obey-voice: live Ollama model %q and worker model %q share a host; the talking session may be evicted. Leave voice_worker_model empty to reuse the live model, or set voice_worker_ollama_host to another machine.", live, worker)
	}
	workerCfg := config.WorkerBrainConfig(cfg)
	name := strings.TrimSpace(strings.ToLower(workerCfg.BrainProvider))
	if !workerProviderAllowed(name) {
		return nil, fmt.Errorf("unsupported voice_worker_provider %q (allowed: %s)", name, strings.Join(config.WorkerProviders, ", "))
	}
	return NewProvider(workerCfg)
}

func workerProviderAllowed(name string) bool {
	return config.WorkerProviderAllowed(name)
}

// WorkerStreamOptions is the tool surface for background jobs: full assistant
// tools, no per-turn voice instruction.
func WorkerStreamOptions(toolsEnabled bool) StreamOptions {
	return StreamOptions{
		ToolsEnabled:        toolsEnabled,
		OmitTurnInstruction: true,
		RouterToolsPolicy:   RouterToolsLegacy,
	}
}
