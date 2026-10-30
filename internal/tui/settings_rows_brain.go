package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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
	if m.cfg.BrainProvider == "obey" {
		return m.obeyRows()
	}
	key := ""
	switch m.cfg.BrainProvider {
	case "ollama":
		key = "ollama_model"
	case "grok":
		key = "grok_model"
	case "cursor":
		key = "cursor_model"
	case "muse-spark":
		key = "muse_spark_model"
	case "codex":
		key = "codex_model"
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

func (m settingsModel) codexRows() []settingRow {
	if m.cfg.BrainProvider != "codex" {
		return nil
	}
	spec, _ := config.SpecFor("codex_reasoning_effort")
	values := append([]string{""}, spec.Enum...)
	labels := append([]string{"Codex default"}, spec.Enum...)
	value := m.cfg.CodexReasoningEffort
	if value == "" {
		value = labels[0]
	}
	return []settingRow{{key: spec.Key, name: "Reasoning effort", kind: rowChoice, value: value, options: labels, current: indexOf(values, m.cfg.CodexReasoningEffort), applies: appliesNextConversation, help: spec.Help,
		set: func(m *settingsModel, i int) tea.Cmd {
			save := m.saveConfig
			if save == nil {
				save = config.SetAndSave
			}
			if err := save(spec.Key, values[i]); err != nil {
				m.message = "Codex setting was not saved: " + err.Error()
				return nil
			}
			m.cfg.CodexReasoningEffort = values[i]
			m.message = "Codex reasoning saved; applies to the next conversation"
			return nil
		}}}
}

// ollamaThinkingRows controls generation, independently of the transcript toggle.
func (m settingsModel) ollamaThinkingRows() []settingRow {
	if m.cfg.BrainProvider != "ollama" {
		return nil
	}
	spec, _ := config.SpecFor("ollama_think")
	values := spec.Enum
	value := m.cfg.OllamaThink
	if value == "" {
		value = "auto"
	}
	return []settingRow{{key: spec.Key, name: "Thinking", kind: rowChoice,
		value: value, options: values, current: indexOf(values, value),
		applies: appliesNextConversation,
		help:    "Model thinking: false = off, true = on; auto currently means off. Levels depend on model support. Separate from showing thinking in chat.",
		set: func(m *settingsModel, i int) tea.Cmd {
			save := m.saveConfig
			if save == nil {
				save = config.SetAndSave
			}
			if err := save(spec.Key, values[i]); err != nil {
				m.message = "Thinking setting was not saved: " + err.Error()
				return nil
			}
			m.cfg.OllamaThink = values[i]
			m.message = "Ollama thinking saved; applies to the next conversation"
			return nil
		}}}
}
