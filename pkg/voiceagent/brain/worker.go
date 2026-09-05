package brain

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// NewWorkerProvider constructs the async job worker as a second Provider
// instance from voice_worker_provider (default grok).
func NewWorkerProvider(cfg *config.Config) (Provider, error) {
	if config.TwoLocalOllamaWorkers(cfg) {
		return nil, fmt.Errorf("voice_worker_provider=ollama on the live Ollama host would load a second local model and can evict the talking session; set voice_worker_provider to grok or claude, or voice_worker_ollama_host to another machine")
	}
	workerCfg := config.WorkerBrainConfig(cfg)
	name := strings.TrimSpace(strings.ToLower(workerCfg.BrainProvider))
	if !workerProviderAllowed(name) {
		return nil, fmt.Errorf("unsupported voice_worker_provider %q (allowed: ollama, grok, claude)", name)
	}
	return NewProvider(workerCfg)
}

func workerProviderAllowed(name string) bool {
	switch name {
	case "ollama", "grok", "claude":
		return true
	default:
		return false
	}
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
