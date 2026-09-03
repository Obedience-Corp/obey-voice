package discovery

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestDiscoverProvidersIncludesAllBackends(t *testing.T) {
	// Unreachable host so the ollama probe fails fast instead of waiting on the network.
	cfg := &config.Config{OllamaHost: "http://127.0.0.1:1"}

	found := map[string]bool{}
	for _, p := range DiscoverProviders(cfg) {
		found[p.Name] = true
	}

	specs := brain.Providers()
	if len(specs) == 0 {
		t.Fatal("brain.Providers() is empty")
	}
	for _, spec := range specs {
		if !found[spec.Name] {
			t.Errorf("DiscoverProviders() missing %q provider; got %v", spec.Name, found)
		}
	}
}
