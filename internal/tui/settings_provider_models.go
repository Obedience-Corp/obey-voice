package tui

import (
	"context"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// discoverProviderModels reads one provider's model list. Tests replace it or
// put a stand-in CLI on PATH.
var discoverProviderModels = func(ctx context.Context, cfg *config.Config, name string) discovery.ProviderInfo {
	return discovery.DiscoverProvider(ctx, cfg, name)
}

// providerModelsMsg carries a provider with its model list.
type providerModelsMsg struct {
	info discovery.ProviderInfo
}

// loadPendingModels reads the model list of each provider the startup scan
// left pending, once per process: Settings is the first screen that needs
// them, and the answers are kept on the App.
func (a App) loadPendingModels() (App, tea.Cmd) {
	var cmds []tea.Cmd
	for _, p := range a.providers {
		if !p.ModelsPending || slices.Contains(a.modelsLoading, p.Name) {
			continue
		}
		a.modelsLoading = append(slices.Clone(a.modelsLoading), p.Name)
		cfg, name := a.cfg.Clone(), p.Name
		cmds = append(cmds, func() tea.Msg {
			return providerModelsMsg{info: discoverProviderModels(context.Background(), cfg, name)}
		})
	}
	return a, tea.Batch(cmds...)
}

func (a App) onProviderModels(msg providerModelsMsg) (tea.Model, tea.Cmd) {
	a.modelsLoading = slices.DeleteFunc(slices.Clone(a.modelsLoading), func(name string) bool { return name == msg.info.Name })
	providers := slices.Clone(a.providers)
	for i := range providers {
		if providers[i].Name == msg.info.Name {
			msg.info.ModelsPending = false
			providers[i] = msg.info
		}
	}
	a.providers = providers
	a.launcher.providers = providers
	a.settings.setProviders(providers)
	return a, nil
}

func (m *settingsModel) setProviders(providers []discovery.ProviderInfo) {
	m.keepFocus(func() {
		m.providers = providers
		m.buildProviderItems()
		m.buildModelItems()
		m.buildWorkerItems()
	})
}

// selectedModelsPending reports whether the model list for the selected provider
// is still being read.
func (m settingsModel) selectedModelsPending() bool {
	for _, p := range m.providers {
		if p.Name == m.cfg.BrainProvider {
			return p.ModelsPending
		}
	}
	return false
}
