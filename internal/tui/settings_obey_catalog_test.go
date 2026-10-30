package tui

import (
	"testing"

	obey "github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestObeyCatalogStaleResultCannotChangeCurrentRoute(t *testing.T) {
	cfg := &config.Config{BrainProvider: "obey", ObeySocket: "/new.sock", ObeyProvider: "saved", ObeyModel: "manual-model", ObeyToolPolicy: "strict"}
	m := settingsModel{cfg: cfg, obeyCatalogSeq: 2, obeyCatalogLoading: true}
	stale := obeyCatalogMsg{seq: 1, route: discovery.ObeyRoute{Socket: "/old.sock", Provider: "old"}}
	m = m.receiveObeyCatalog(stale)
	if m.obeyCatalog != nil || !m.obeyCatalogLoading || cfg.ObeyModel != "manual-model" {
		t.Fatal("stale catalog affected current route")
	}
	current := obeyCatalogMsg{seq: 2, route: discovery.ObeyRoute{Socket: "/new.sock", Provider: "saved", Model: "manual-model", ToolPolicy: "strict", Models: &obey.ModelCatalog{Models: []obey.ModelInfo{{ID: "catalog-model"}}}}}
	m = m.receiveObeyCatalog(current)
	if m.obeyCatalog == nil || m.obeyCatalogLoading || cfg.ObeyModel != "manual-model" {
		t.Fatal("catalog did not retain manual selection")
	}
	cfg.ObeySocket = "/third.sock"
	if got := m.receiveObeyCatalog(current); got.obeyCatalog != m.obeyCatalog {
		t.Fatal("scope mismatch replaced catalog")
	}
}

func TestObeyCatalogOnlyLoadsForSelectedBackend(t *testing.T) {
	m := settingsModel{cfg: &config.Config{BrainProvider: "ollama"}}
	if cmd := m.loadObeyCatalogIfSelected(); cmd != nil || m.obeyCatalogLoading || m.obeyCatalogCancel != nil {
		t.Fatal("native provider started Obey discovery")
	}
	m.cfg.BrainProvider = "obey"
	cmd := m.loadObeyCatalogIfSelected()
	if cmd == nil || !m.obeyCatalogLoading || m.obeyCatalogCancel == nil {
		t.Fatal("selected Obey did not schedule catalog lookup")
	}
	m.cancelObeyCatalog()
	if m.obeyCatalogLoading || m.obeyCatalogCancel != nil {
		t.Fatal("catalog request was not canceled")
	}
}
