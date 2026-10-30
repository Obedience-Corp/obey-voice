package tui

import (
	"context"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func (m settingsModel) obeyRows() []settingRow {
	rows := []settingRow{{
		name: "Obey routing", kind: rowInfo, value: "Obey → provider → model",
		help: "Obey serves multiple providers. Enter the provider ID configured in your daemon, then a model ID for that provider. Obey is optional; other brain providers work without it.",
	}, {
		name: "Model selection", kind: rowInfo, value: "Catalog choices and manual IDs",
		help: "Load the selected daemon catalog with Obey 0.5.0+. Manual IDs remain available for older CLIs or incomplete catalogs.",
	}}

	rows = append(rows, settingRow{name: "Load daemon catalog", kind: rowAction, value: "providers, models and readiness", disabled: m.obeyCatalogLoading, reason: "Reading the selected daemon…", run: func(m *settingsModel) tea.Cmd { return m.loadObeyCatalog(false) }})
	if catalog := m.obeyCatalog; catalog != nil {
		if catalog.Problem != "" {
			rows = append(rows, settingRow{name: "Catalog", kind: rowInfo, value: catalog.Problem, help: "Keep manual IDs; install Obey through Festival or check the selected daemon."})
		}
		if catalog.Providers != nil {
			ids := []string{}
			for _, profile := range catalog.Providers.Providers {
				ids = append(ids, profile.ID)
			}
			if len(ids) > 0 {
				rows = append(rows, settingRow{name: "Choose daemon provider", kind: rowPick, options: ids, current: indexOf(ids, m.cfg.ObeyProvider), set: func(m *settingsModel, i int) tea.Cmd {
					m.commitObeyEdit("obey_provider", ids[i])
					return m.loadObeyCatalog(false)
				}})
			}
		}
		if catalog.Models != nil {
			ids := []string{}
			for _, model := range catalog.Models.Models {
				ids = append(ids, model.ID)
			}
			if len(ids) > 0 {
				rows = append(rows, settingRow{name: "Choose daemon model", kind: rowPick, options: ids, current: indexOf(ids, m.cfg.ObeyModel), help: "Catalog membership does not guarantee inference. Manual IDs remain valid for incomplete catalogs.", set: func(m *settingsModel, i int) tea.Cmd {
					m.commitObeyEdit("obey_model", ids[i])
					return m.loadObeyCatalog(false)
				}})
			}
			rows = append(rows, settingRow{name: "Catalog freshness", kind: rowInfo, value: catalog.Models.Source + " · " + catalog.Models.State}, settingRow{name: "Refresh daemon models", kind: rowAction, disabled: m.obeyCatalogLoading, run: func(m *settingsModel) tea.Cmd { return m.loadObeyCatalog(true) }})
		}
		if catalog.Readiness != nil {
			for _, check := range catalog.Readiness.Checks {
				rows = append(rows, settingRow{name: check.Stage, kind: rowInfo, value: string(check.State) + " · " + check.Code, help: check.Remediation})
			}
		}
	}
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
	m.cancelObeyCatalog()
	m.obeyCatalog = nil
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

type obeyCatalogMsg struct {
	seq   int
	route discovery.ObeyRoute
}

func (m *settingsModel) cancelObeyCatalog() {
	if m.obeyCatalogCancel != nil {
		m.obeyCatalogCancel()
		m.obeyCatalogCancel = nil
	}
	m.obeyCatalogSeq++
	m.obeyCatalogLoading = false
}
func (m *settingsModel) loadObeyCatalog(refresh bool) tea.Cmd {
	m.cancelObeyCatalog()
	ctx, cancel := context.WithCancel(context.Background())
	m.obeyCatalogCancel = cancel
	m.obeyCatalogLoading = true
	cfg, seq := *m.cfg, m.obeyCatalogSeq
	return func() tea.Msg {
		defer cancel()
		return obeyCatalogMsg{seq: seq, route: discovery.DiscoverObeyRoute(ctx, &cfg, refresh)}
	}
}
func (m settingsModel) receiveObeyCatalog(msg obeyCatalogMsg) settingsModel {
	route := msg.route
	if msg.seq != m.obeyCatalogSeq || m.cfg.BrainProvider != "obey" || route.Socket != m.cfg.ObeySocket || route.Provider != m.cfg.ObeyProvider || route.Model != m.cfg.ObeyModel || route.ToolPolicy != m.cfg.ObeyToolPolicy {
		return m
	}
	m.obeyCatalogLoading = false
	m.obeyCatalog = &route
	m.clampCursor()
	return m
}

func (m *settingsModel) loadObeyCatalogIfSelected() tea.Cmd {
	if m.cfg.BrainProvider != "obey" {
		return nil
	}
	return m.loadObeyCatalog(false)
}
