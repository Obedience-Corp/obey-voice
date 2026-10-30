package discovery

import (
	"context"
	"strings"

	obey "github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// ObeyRoute identifies the configuration that produced a snapshot. UIs must
// discard a response if any route field has changed while it was pending.
type ObeyRoute struct {
	Socket     string                `json:"socket"`
	Provider   string                `json:"provider"`
	Model      string                `json:"model"`
	ToolPolicy string                `json:"tool_policy"`
	Providers  *obey.ProviderCatalog `json:"providers,omitempty"`
	Models     *obey.ModelCatalog    `json:"models,omitempty"`
	Readiness  *obey.Readiness       `json:"readiness,omitempty"`
	Problem    string                `json:"problem,omitempty"`
}

// DiscoverObeyRoute reads only the selected daemon. Catalog failure never
// rewrites saved IDs, probes a direct provider, starts a daemon or submits work.
func DiscoverObeyRoute(parent context.Context, cfg *config.Config, refresh bool) ObeyRoute {
	out := ObeyRoute{Socket: cfg.ObeySocket, Provider: cfg.ObeyProvider, Model: cfg.ObeyModel, ToolPolicy: cfg.ObeyToolPolicy}
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	client, err := obey.NewClientFromPath(obey.WithSocket(cfg.ObeySocket))
	if err != nil {
		out.Problem = err.Error()
		return out
	}
	defer client.Close()
	out.Providers, err = client.Providers(ctx)
	if err != nil {
		out.Problem = err.Error()
		return out
	}
	if strings.TrimSpace(cfg.ObeyProvider) == "" {
		return out
	}
	if refresh {
		out.Models, err = client.RefreshModels(ctx, cfg.ObeyProvider)
	} else {
		out.Models, err = client.Models(ctx, cfg.ObeyProvider)
	}
	if err != nil {
		out.Problem = err.Error()
		return out
	}
	policy := obey.ToolPolicyOff
	if cfg.ObeyToolPolicy == "daemon" {
		policy = obey.ToolPolicyDaemonManaged
	}
	out.Readiness, err = client.Preflight(ctx, obey.PreflightRequest{Provider: cfg.ObeyProvider, Model: cfg.ObeyModel, ToolPolicy: policy})
	if err != nil {
		out.Problem = err.Error()
	}
	return out
}

func discoverObey(ctx context.Context, cfg *config.Config) ProviderInfo {
	client, err := obey.NewClientFromPath()
	info := ProviderInfo{Name: "obey", Available: err == nil}
	if client != nil {
		_ = client.Close()
	}
	if cfg == nil {
		return info
	}
	if model := strings.TrimSpace(cfg.ObeyModel); model != "" {
		info.Models = []string{model}
	}
	// Other backends remain usable without any daemon probe.
	if cfg.BrainProvider != "obey" || !info.Available {
		return info
	}
	route := DiscoverObeyRoute(ctx, cfg, false)
	if route.Models != nil {
		for _, model := range route.Models.Models {
			if !containsModel(info.Models, model.ID) {
				info.Models = append(info.Models, model.ID)
			}
		}
	}
	return info
}

func containsModel(models []string, id string) bool {
	for _, model := range models {
		if model == id {
			return true
		}
	}
	return false
}
