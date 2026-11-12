package discovery

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestDiscoverProvidersIncludesAllBackends(t *testing.T) {
	// No provider CLI is reachable, so no installed one is started, and the
	// unreachable host makes the ollama probe fail fast.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
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

func TestClaudeDiscoveryListsTheConfiguredModelOnce(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for model, want := range map[string]string{
		"":          "default",
		"haiku":     "default,haiku",
		" haiku ":   "default,haiku",
		"default":   "default",
		" DEFAULT ": "default",
	} {
		info := DiscoverProvider(t.Context(), &config.Config{ClaudeModel: model}, "claude")
		if got := strings.Join(info.Models, ","); got != want {
			t.Errorf("claude_model %q: models = %s, want %s", model, got, want)
		}
	}
}
