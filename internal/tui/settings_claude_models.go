package tui

import (
	"context"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// discoverClaudeModels reads the Claude CLI's model list. Tests replace it or
// put a stand-in CLI on PATH.
var discoverClaudeModels = func(ctx context.Context, cfg *config.Config) discovery.ProviderInfo {
	return discovery.DiscoverProvider(ctx, cfg, "claude")
}

// claudeModelsMsg carries the Claude provider with its model list.
type claudeModelsMsg struct {
	info discovery.ProviderInfo
}

func claudeModelsPending(providers []discovery.ProviderInfo) bool {
	return slices.ContainsFunc(providers, func(p discovery.ProviderInfo) bool { return p.ModelsPending })
}

// loadClaudeModels asks the Claude CLI for its models once per process: the
// startup scan leaves them pending, Settings is the first screen that needs
// them, and the answer is kept on the App.
func (a App) loadClaudeModels() (App, tea.Cmd) {
	if a.claudeModelsLoading || !claudeModelsPending(a.providers) {
		return a, nil
	}
	a.claudeModelsLoading = true
	cfg := a.cfg.Clone()
	return a, func() tea.Msg {
		return claudeModelsMsg{info: discoverClaudeModels(context.Background(), cfg)}
	}
}

func (a App) onClaudeModels(msg claudeModelsMsg) (tea.Model, tea.Cmd) {
	a.claudeModelsLoading = false
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
