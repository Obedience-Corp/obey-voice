package tui

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func (m settingsModel) obeyRows() []settingRow {
	var rows []settingRow
	for _, key := range []string{"obey_provider", "obey_model", "obey_camp", "obey_socket", "obey_tool_policy"} {
		spec, _ := config.SpecFor(key)
		value := m.obeyValue(key)
		rows = append(rows, settingRow{key: key, name: spec.Title, kind: rowText, value: value,
			applies: appliesNextConversation, help: spec.Help,
			run: func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editObey, spec.Title, m.obeyValue(key))
				m.edit.decisionKey = key
				return cmd
			},
		})
	}
	return rows
}
func (m settingsModel) obeyValue(key string) string {
	switch key {
	case "obey_provider":
		return m.cfg.ObeyProvider
	case "obey_model":
		return m.cfg.ObeyModel
	case "obey_camp":
		return m.cfg.ObeyCamp
	case "obey_socket":
		return m.cfg.ObeySocket
	case "obey_tool_policy":
		if m.cfg.ObeyToolPolicy == "" {
			return "strict"
		}
		return m.cfg.ObeyToolPolicy
	}
	return ""
}
func (m *settingsModel) commitObeyEdit(key, value string) {
	value = strings.TrimSpace(value)
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save(key, value); err != nil {
		m.message = "Obey setting was not saved: " + err.Error()
		return
	}
	switch key {
	case "obey_provider":
		m.cfg.ObeyProvider = value
	case "obey_model":
		m.cfg.ObeyModel = value
	case "obey_camp":
		m.cfg.ObeyCamp = value
	case "obey_socket":
		m.cfg.ObeySocket = value
	case "obey_tool_policy":
		m.cfg.ObeyToolPolicy = value
	}
	m.message = "Obey setting saved; applies to the next conversation"
}
