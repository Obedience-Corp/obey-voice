package discovery

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestObeyOptionalDiscoveryDoesNotHideOtherProviders(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"models":[{"name":"local-model:latest"}]}`)
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, ObeyProvider: "saved-provider", ObeyModel: "saved-model"}
	var local, daemon ProviderInfo
	for _, p := range DiscoverProvidersContext(t.Context(), cfg) {
		switch p.Name {
		case "ollama":
			local = p
		case "obey":
			daemon = p
		}
	}
	if !local.Available || len(local.Models) != 1 || local.Models[0] != "local-model" {
		t.Fatalf("local provider lost: %+v", local)
	}
	if daemon.Name != "obey" || daemon.Available {
		t.Fatalf("missing Obey was advertised as installed: %+v", daemon)
	}
	if cfg.ObeyProvider != "saved-provider" || cfg.ObeyModel != "saved-model" {
		t.Fatal("discovery changed saved config")
	}
}
