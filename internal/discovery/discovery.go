package discovery

import (
	"context"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// ProviderInfo describes an available brain provider.
type ProviderInfo struct {
	Name      string
	Available bool
	Models    []string
	// WorkerModels contains verified completion+tools models from the worker's
	// Ollama endpoint. Nil means unavailable; an empty slice means none qualify.
	WorkerModels []string
}

// ForWorkers preserves other providers' catalogs and restricts Ollama choices
// to verified tool-capable models, including when its worker uses another host.
func (p ProviderInfo) ForWorkers() ProviderInfo {
	if strings.EqualFold(p.Name, "ollama") {
		p.Available = p.WorkerModels != nil
		p.Models = p.WorkerModels
	}
	return p
}

// probeTimeout caps each network/subprocess probe so a wedged provider cannot
// stall discovery.
var probeTimeout = defaultProbeTimeout

const defaultProbeTimeout = 3 * time.Second

// DiscoverProviders probes the system for available brain providers.
func DiscoverProviders(cfg *config.Config) []ProviderInfo {
	return DiscoverProvidersContext(context.Background(), cfg)
}

// DiscoverProvidersContext is DiscoverProviders with caller-owned cancellation:
// the probes stop when ctx is done, so a UI that opens a form can abandon the
// scan when the form closes. Providers are always reported (a failed probe is
// an unavailable provider, not a missing row).
func DiscoverProvidersContext(ctx context.Context, cfg *config.Config) []ProviderInfo {
	specs := brain.Providers()
	out := make([]ProviderInfo, 0, len(specs))
	for _, spec := range specs {
		out = append(out, probeProvider(ctx, cfg, spec.Name))
	}
	return out
}

// DiscoverProvider probes one brain provider, as DiscoverProvidersContext
// does for each.
func DiscoverProvider(ctx context.Context, cfg *config.Config, name string) ProviderInfo {
	return probeProvider(ctx, cfg, name)
}

func probeProvider(ctx context.Context, cfg *config.Config, name string) ProviderInfo {
	switch name {
	case "muse-spark":
		_, err := exec.LookPath("muse")
		models := []string{"default"}
		if cfg != nil && strings.TrimSpace(cfg.MuseSparkModel) != "" {
			models = append(models, cfg.MuseSparkModel)
		}
		return ProviderInfo{Name: name, Available: err == nil, Models: models}
	case "claude":
		return discoverClaude(ctx, cfg)
	case "cursor":
		return discoverCursor(ctx)
	case "obey":
		return discoverObey(ctx, cfg)
	case "codex":
		return discoverCodex(ctx)
	case "grok":
		return discoverGrok(ctx)
	case "ollama":
		return discoverOllama(ctx, cfg)
	default:
		return ProviderInfo{Name: name}
	}
}

// discoverClaude lists "default", then the models the installed claude CLI
// reports, then the saved claude_model if the CLI did not report it. A CLI that
// cannot answer (too old, signed out, too slow) leaves "default" and the saved
// model. Nothing is cached: what "default" resolves to can change.
func discoverClaude(parent context.Context, cfg *config.Config) ProviderInfo {
	info := ProviderInfo{Name: "claude", Models: []string{"default"}}
	if _, err := exec.LookPath("claude"); err == nil {
		info.Available = true
		if parent.Err() == nil {
			ctx, cancel := context.WithTimeout(parent, probeTimeout)
			reported, err := brain.ClaudeModels(ctx, cfg)
			cancel()
			if err == nil {
				for _, model := range reported {
					info.Models = appendModel(info.Models, model)
				}
			}
		}
	}
	if cfg != nil {
		info.Models = appendModel(info.Models, cfg.ClaudeModel)
	}
	return info
}

// appendModel adds model unless it is blank or already listed in any case.
func appendModel(models []string, model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return models
	}
	for _, listed := range models {
		if strings.EqualFold(listed, model) {
			return models
		}
	}
	return append(models, model)
}

func discoverCursor(parent context.Context) ProviderInfo {
	info := ProviderInfo{Name: "cursor"}

	binPath, err := cursor.LocateBinary()
	if err != nil {
		return info
	}
	info.Available = true

	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	models, err := cursor.NewClient(binPath).Models(ctx)
	if err != nil {
		info.Models = []string{"default"}
		return info
	}
	for _, mod := range models {
		info.Models = append(info.Models, mod.ID)
	}
	if len(info.Models) == 0 {
		info.Models = []string{"default"}
	}

	return info
}

func discoverGrok(parent context.Context) ProviderInfo {
	info := ProviderInfo{Name: "grok"}

	binPath, err := grok.LocateBinary()
	if err != nil {
		return info
	}
	info.Available = true

	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	models, err := grok.NewClient(binPath).Models(ctx)
	if err != nil {
		info.Models = []string{"default"}
		return info
	}
	for _, mod := range models {
		info.Models = append(info.Models, mod.ID)
	}
	if len(info.Models) == 0 {
		info.Models = []string{"default"}
	}

	return info
}

func discoverOllama(parent context.Context, cfg *config.Config) ProviderInfo {
	info := ProviderInfo{Name: "ollama"}
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	workerHost := strings.TrimSpace(cfg.VoiceWorkerOllamaHost)
	base, err := url.Parse(cfg.OllamaHost)
	var names []string
	if err == nil {
		client := api.NewClient(base, http.DefaultClient)
		if resp, listErr := client.List(ctx); listErr == nil {
			info.Available = true
			for _, m := range resp.Models {
				names = append(names, m.Name)
				info.Models = append(info.Models, strings.TrimSuffix(m.Name, ":latest"))
			}
			if workerHost == "" || workerHost == cfg.OllamaHost {
				info.WorkerModels = ollamaWorkerModels(ctx, client, names)
				return info
			}
		}
	}
	if workerHost != "" && workerHost != cfg.OllamaHost {
		if workerBase, parseErr := url.Parse(workerHost); parseErr == nil {
			client := api.NewClient(workerBase, http.DefaultClient)
			if resp, listErr := client.List(ctx); listErr == nil {
				names = nil
				for _, m := range resp.Models {
					names = append(names, m.Name)
				}
				info.WorkerModels = ollamaWorkerModels(ctx, client, names)
			}
		}
	}
	return info
}
