package brain

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// ProviderSpec describes a brain provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

type providerEntry struct {
	spec  config.BrainProviderSpec
	new   func(*config.Config) (Provider, error)
	batch func(*config.Config) (BatchProvider, error)
}

var registry []providerEntry

func register(
	spec config.BrainProviderSpec,
	newFn func(*config.Config) (Provider, error),
	batchFn func(*config.Config) (BatchProvider, error),
) {
	config.RegisterBrainProvider(spec)
	registry = append(registry, providerEntry{spec: spec, new: newFn, batch: batchFn})
}

// Providers returns the list of implemented brain providers, in registration
// order. Adding a backend is registering it from that backend's file, not
// appending a name here.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, 0, len(registry))
	for _, e := range registry {
		out = append(out, ProviderSpec{Name: e.spec.Name, Description: e.spec.Description})
	}
	return out
}

// NewProvider constructs the configured brain provider.
func NewProvider(cfg *config.Config) (Provider, error) {
	name := normalizeProvider(cfg.BrainProvider)
	if name == "" {
		name = "ollama"
	}
	if cfg.DelegationEnabled && name != "ollama" {
		return nil, fmt.Errorf("delegation requires the Ollama brain")
	}
	for _, e := range registry {
		if e.spec.Name == name {
			provider, err := e.new(cfg)
			if err != nil {
				return nil, err
			}
			if cfg.DelegationEnabled {
				o, ok := provider.(*OllamaBrain)
				if !ok {
					return nil, fmt.Errorf("delegation requires the Ollama brain")
				}
				if err := o.configureDelegation(cfg); err != nil {
					return nil, err
				}
			}
			return provider, nil
		}
	}
	return nil, unsupportedProviderError("brain_provider", cfg.BrainProvider, Providers())
}

// NewBatchProvider constructs the configured provider's batch adapter for
// single-shot, history-free transforms. It mirrors NewProvider's provider
// selection, so the default ("" / "ollama") config resolves to Ollama.
func NewBatchProvider(cfg *config.Config) (BatchProvider, error) {
	name := normalizeProvider(cfg.BrainProvider)
	if name == "" {
		name = "ollama"
	}
	for _, e := range registry {
		if e.spec.Name == name {
			return e.batch(cfg)
		}
	}
	return nil, unsupportedProviderError("batch brain_provider", cfg.BrainProvider, Providers())
}

func normalizeProvider(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}

func unsupportedProviderError(key, configured string, specs []ProviderSpec) error {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported %s %q (implemented providers: %s)", key, configured, strings.Join(names, ", "))
}
