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
}

// probeTimeout caps each network/subprocess probe so a wedged provider cannot
// stall discovery.
const probeTimeout = 3 * time.Second

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

func probeProvider(ctx context.Context, cfg *config.Config, name string) ProviderInfo {
	switch name {
	case "claude":
		return discoverClaude()
	case "cursor":
		return discoverCursor(ctx)
	case "obey":
		return discoverObey(cfg)
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

func discoverClaude() ProviderInfo {
	_, err := exec.LookPath("claude")
	return ProviderInfo{
		Name:      "claude",
		Available: err == nil,
		Models:    []string{"default"},
	}
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

	base, err := url.Parse(cfg.OllamaHost)
	if err != nil {
		return info
	}

	client := api.NewClient(base, http.DefaultClient)

	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	resp, err := client.List(ctx)
	if err != nil {
		return info
	}

	info.Available = true
	for _, m := range resp.Models {
		info.Models = append(info.Models, strings.TrimSuffix(m.Name, ":latest"))
	}

	return info
}
