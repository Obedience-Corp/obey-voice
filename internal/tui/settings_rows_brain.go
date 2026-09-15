package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m settingsModel) providerRows() []settingRow {
	current := -1
	muted := make([]bool, len(m.providers))
	for i, p := range m.providers {
		if p.Name == m.cfg.BrainProvider {
			current = i
		}
		muted[i] = !p.Available
	}
	value := strings.TrimSpace(m.cfg.BrainProvider)
	if value == "" {
		value = "not set"
	}
	return []settingRow{{
		key: "brain_provider", name: "Provider", kind: rowPick,
		value: value, options: m.providerItems, current: current, muted: muted,
		applies: appliesNextConversation,
		help:    "Which provider answers you. Choosing one lands on Worker if none is set.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.selectProviderOption(i) },
	}}
}

func (m settingsModel) modelRows() []settingRow {
	key := ""
	switch m.cfg.BrainProvider {
	case "ollama":
		key = "ollama_model"
	case "grok":
		key = "grok_model"
	case "cursor":
		key = "cursor_model"
	}
	active := m.activeModel()
	models := m.modelItems
	row := settingRow{
		key: key, name: "Model", kind: rowPick,
		value: active, options: models, current: indexOf(models, active),
		applies: appliesNextConversation,
		help:    "Which model thinks for the brain provider.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.selectModel(models[i]) },
	}
	if key == "" {
		row.detail = "not routed for " + m.cfg.BrainProvider
	}
	return []settingRow{row}
}
