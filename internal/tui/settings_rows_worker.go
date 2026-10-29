package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func (m settingsModel) workerRows() []settingRow {
	if (m.workerDraft != nil && m.workerDraft.Mode == config.WorkerModeTiered) ||
		(m.workerDraft == nil && m.cfg != nil && m.cfg.VoiceWorkers != nil && m.cfg.VoiceWorkers.Mode == config.WorkerModeTiered) {
		return m.tierWorkerRows()
	}
	return append(m.legacyWorkerRows(), settingRow{key: "worker_mode", name: "Worker mode", kind: rowInfo, value: "Legacy", detail: "current worker and persona overrides"})
}

func (m settingsModel) legacyWorkerRows() []settingRow {
	infos := m.workerProviderInfos()
	names := make([]string, 0, len(infos))
	labels := make([]string, 0, len(infos))
	muted := make([]bool, 0, len(infos))
	for _, p := range infos {
		label := p.Name
		if !p.Available {
			label += " (not available)"
		}
		names = append(names, p.Name)
		labels = append(labels, label)
		muted = append(muted, !p.Available)
	}
	active := m.activeWorkerProvider()
	provider := settingRow{
		key: "voice_worker_provider", name: "Provider", kind: rowPick,
		value: active, options: labels, current: indexOf(names, active), muted: muted,
		applies: appliesNextConversation,
		help:    "The second brain: runs the background jobs a spawn starts, with full tools.",
		set: func(m *settingsModel, i int) tea.Cmd {
			m.setWorkerProvider(names[i])
			return nil
		},
	}
	if active == "" {
		provider.value = "not set"
		provider.detail = fmt.Sprintf("inheriting %s until you choose", config.VoiceWorkerProviderOf(m.cfg))
	}
	models := m.workerModelChoices()
	model := settingRow{
		key: "voice_worker_model", name: "Model", kind: rowPick,
		value: m.activeWorkerModel(), options: models, current: -1,
		applies: appliesNextConversation,
		help:    "Ollama lists verified tool-capable models only. Default clears the pin; inherited models are checked before running.",
		set: func(m *settingsModel, i int) tea.Cmd {
			m.setWorkerModel(models[i])
			return nil
		},
	}
	if config.WorkerSelected(m.cfg) {
		model.current = indexOf(models, m.activeWorkerModel())
	}
	if active == "ollama" && m.activeWorkerModel() != "default" && model.current < 0 {
		model.detail = "Saved model has no verified tool support; choose another worker model."
	}
	permission := settingRow{
		key: "voice_workers_disabled", name: "Allow background workers", kind: rowToggle,
		value: onOff(!m.cfg.VoiceWorkersDisabled), applies: appliesNow,
		help: "Off blocks new background jobs. Existing tasks may finish; /workers has Stop task and Stop all.",
		run: func(m *settingsModel) tea.Cmd {
			enabled := m.cfg.VoiceWorkersDisabled
			if err := saveWorkersEnabled(m.cfg, enabled, m.saveConfig, m.onWorkersEnabled); err != nil {
				m.message = "Failed to save worker preference: " + err.Error()
			} else {
				m.message = workersPermissionMessage(enabled)
			}
			return nil
		},
	}
	rows := []settingRow{provider, model}
	rows = append(rows, settingRow{key: "worker_migration", name: "Use current worker for all tiers", kind: rowAction,
		detail: "preview", applies: appliesNextConversation, help: "Create a tiered draft from the resolved legacy worker. Nothing is saved until Save.",
		run: func(m *settingsModel) tea.Cmd { return m.previewWorkerMigration() }})
	rows = append(rows, settingRow{key: "worker_effective_bounds", name: "Effective worker limits", kind: rowInfo,
		value:  fmt.Sprintf("legacy: %d concurrent · %s/job", max(1, m.cfg.VoiceWorkerMaxConcurrent), legacyWorkerBudgetLabel(m.cfg.VoiceWorkerJobBudget)),
		detail: "legacy singleton controls; tier policy is not active",
		help:   "These singleton settings control jobs until an explicit tiered worker policy is saved."})
	rows = append(rows, permission)
	return rows
}

// tierWorkerRows deliberately has one assignment row per stable tier. The
// options are discovered from configured backends and are only draft edits.
func (m settingsModel) tierWorkerRows() []settingRow {
	d := m.workerDraft
	if d == nil {
		d = m.cfg.VoiceWorkers
	}
	rows := make([]settingRow, 0, 8)
	for _, name := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex} {
		t := d.Tiers.Simple
		switch name {
		case config.WorkerTierMedium:
			t = d.Tiers.Medium
		case config.WorkerTierComplex:
			t = d.Tiers.Complex
		}
		value := "Disabled"
		if t.Enabled && t.Assignment == nil {
			value = "Unassigned — choose provider/model"
		}
		if t.Enabled && t.Assignment != nil {
			value = t.Assignment.Provider + " / " + t.Assignment.Model
			if reason := workerTierUnavailable(d, name); reason != "" {
				value += " — unavailable: " + reason
			}
		}
		rowName := workerTierTitle(string(name))
		rows = append(rows, settingRow{key: "worker_" + string(name), name: rowName, kind: rowAction, value: value,
			applies: "new jobs after Save", help: "Enter to edit provider, model, references, and limits.",
			run: func(m *settingsModel) tea.Cmd { return m.beginTierEdit(name) }})
		scope, scopeDetail := workerCapabilityScope(t.Assignment)
		rows = append(rows,
			settingRow{key: "worker_" + string(name) + "_enabled", name: rowName + " enabled", kind: rowToggle, value: onOff(t.Enabled), run: func(m *settingsModel) tea.Cmd { t := m.tierDraft(name); t.Enabled = !t.Enabled; return nil }},
			settingRow{key: "worker_" + string(name) + "_uncapped", name: rowName + " uncapped", kind: rowToggle, value: onOff(t.AllowUncapped), detail: "explicit opt-in; charge cap unavailable", run: func(m *settingsModel) tea.Cmd { t := m.tierDraft(name); t.AllowUncapped = !t.AllowUncapped; return nil }},
			workerBillingRow(name, rowName, t.Assignment),
			settingRow{key: "worker_" + string(name) + "_endpoint", name: rowName + " endpoint reference", kind: rowAction, value: workerRef(t.Assignment, true), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerReferenceEdit(name, "endpoint reference", workerRef(t.Assignment, true))
			}},
			settingRow{key: "worker_" + string(name) + "_account", name: rowName + " account reference", kind: rowAction, value: workerRef(t.Assignment, false), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerReferenceEdit(name, "account reference", workerRef(t.Assignment, false))
			}},
			settingRow{key: "worker_" + string(name) + "_capabilities", name: rowName + " enforcing scope", kind: rowInfo, value: scope, detail: scopeDetail},
			settingRow{key: "worker_" + string(name) + "_limits", name: rowName + " limits", kind: rowInfo, value: fmt.Sprintf("%ds · %d requests · %d tools", t.Limits.RuntimeSeconds, t.Limits.ModelRequests, t.Limits.ToolCalls)},
			settingRow{key: "worker_" + string(name) + "_runtime", name: rowName + " runtime seconds", kind: rowAction, value: fmt.Sprint(t.Limits.RuntimeSeconds), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerLimitEdit(name, "runtime_seconds", t.Limits.RuntimeSeconds)
			}},
			settingRow{key: "worker_" + string(name) + "_requests", name: rowName + " model requests", kind: rowAction, value: fmt.Sprint(t.Limits.ModelRequests), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerLimitEdit(name, "model_requests", t.Limits.ModelRequests)
			}},
			settingRow{key: "worker_" + string(name) + "_tools", name: rowName + " tool calls", kind: rowAction, value: fmt.Sprint(t.Limits.ToolCalls), run: func(m *settingsModel) tea.Cmd { return m.beginWorkerLimitEdit(name, "tool_calls", t.Limits.ToolCalls) }},
			settingRow{key: "worker_" + string(name) + "_retries", name: rowName + " transport retries", kind: rowAction, value: fmt.Sprint(t.Limits.TransportRetries), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerLimitEdit(name, "transport_retries", t.Limits.TransportRetries)
			}},
			settingRow{key: "worker_" + string(name) + "_output", name: rowName + " output tokens", kind: rowAction, value: workerIntPtr(t.Limits.OutputTokens), run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerLimitTextEdit(name, "output_tokens", workerIntPtr(t.Limits.OutputTokens))
			}},
			settingRow{key: "worker_" + string(name) + "_charge", name: rowName + " charge cap", kind: rowAction, value: workerBudget(t.Limits.ChargeMicroUSD), detail: "charge cap unavailable", run: func(m *settingsModel) tea.Cmd {
				return m.beginWorkerLimitTextEdit(name, "charge_micro_usd", workerBudget(t.Limits.ChargeMicroUSD))
			}},
		)
	}
	rows = append(rows,
		settingRow{key: "worker_paid_enabled", name: "Paid workers", kind: rowToggle, value: onOff(d.Policy.PaidEnabled), run: func(m *settingsModel) tea.Cmd {
			m.ensureWorkerDraft().Policy.PaidEnabled = !m.ensureWorkerDraft().Policy.PaidEnabled
			return nil
		}},
		settingRow{key: "worker_charge_policy", name: "Charge policy", kind: rowChoice, value: d.Policy.ChargePolicy, options: []string{"advisory", "strict"}, current: indexOf([]string{"advisory", "strict"}, d.Policy.ChargePolicy), set: func(m *settingsModel, i int) tea.Cmd {
			m.ensureWorkerDraft().Policy.ChargePolicy = []string{"advisory", "strict"}[i]
			return nil
		}},
		settingRow{key: "worker_daily_budget", name: "Daily worker budget", kind: rowAction, value: workerBudget(d.Policy.DailyChargeMicroUSD), detail: "micro-USD", run: func(m *settingsModel) tea.Cmd {
			return m.beginWorkerPolicyEdit("daily_charge_micro_usd", workerBudget(d.Policy.DailyChargeMicroUSD))
		}},
		settingRow{key: "worker_timezone", name: "Worker timezone", kind: rowAction, value: d.Policy.Timezone, run: func(m *settingsModel) tea.Cmd { return m.beginWorkerPolicyEdit("timezone", d.Policy.Timezone) }},
		settingRow{key: "worker_queued", name: "Queued workers", kind: rowAction, value: fmt.Sprint(d.Policy.QueuedWorkers), run: func(m *settingsModel) tea.Cmd {
			return m.beginWorkerPolicyEdit("queued_workers", fmt.Sprint(d.Policy.QueuedWorkers))
		}},
		settingRow{key: "worker_concurrent", name: "Concurrent workers", kind: rowChoice, value: fmt.Sprint(d.Policy.ConcurrentWorkers), options: []string{"1", "2", "4", "8"}, current: indexOf([]string{"1", "2", "4", "8"}, fmt.Sprint(d.Policy.ConcurrentWorkers)), set: func(m *settingsModel, i int) tea.Cmd {
			m.ensureWorkerDraft().Policy.ConcurrentWorkers = []int{1, 2, 4, 8}[i]
			return nil
		}},
		settingRow{key: "worker_escalation", name: "Automatic escalation", kind: rowInfo, value: "Never", detail: "fixed policy"},
		settingRow{key: "worker_save", name: "Save worker settings", kind: rowAction, run: func(m *settingsModel) tea.Cmd { return m.saveWorkerDraft() }},
		settingRow{key: "worker_reload", name: "Reload saved worker settings", kind: rowAction, detail: "discards draft edits", run: func(m *settingsModel) tea.Cmd { return m.reloadWorkerDraft() }},
		settingRow{key: "worker_cancel", name: "Cancel worker edits", kind: rowAction, run: func(m *settingsModel) tea.Cmd { m.cancelWorkerDraft(); return nil }})
	for i := range rows {
		row := &rows[i]
		if row.key == "worker_paid_enabled" {
			row.detail = "requires explicit account and policy opt-in"
		}
		switch row.key {
		case "worker_charge_policy":
			if d.Policy.ChargePolicy == "strict" {
				row.detail = "strict paid dispatch blocked: charge cap unavailable"
			} else {
				row.detail = "advisory daily cap; actual charge may be unknown"
			}
		case "worker_daily_budget":
			row.detail = "usage ledger at installation scope; unknown charges remain unknown"
		case "worker_timezone":
			row.detail = "usage ledger bucket timezone"
		}
	}
	rows = append(rows, settingRow{key: "worker_allowance", name: "Worker allowance", kind: rowAction,
		value: m.workerAllowanceValue(), detail: "Enter to inspect; unknown charges remain unknown",
		run: func(m *settingsModel) tea.Cmd { return m.inspectWorkerAllowance() }})
	rows = append(rows, settingRow{key: "worker_current_bounds", name: "Current configured bounds", kind: rowInfo,
		value:  fmt.Sprintf("tiered: %d concurrent · %d queued · per-tier runtime", d.Policy.ConcurrentWorkers, d.Policy.QueuedWorkers),
		detail: "tiered draft; Save applies these limits",
		help:   "Tiered mode replaces legacy singleton controls. Admitted jobs retain their tier limits; remote termination may remain unknown."})
	if m.workerEditingTier == "" {
		filtered := rows[:0]
		for _, row := range rows {
			if strings.HasPrefix(row.key, "worker_simple_") || strings.HasPrefix(row.key, "worker_medium_") || strings.HasPrefix(row.key, "worker_complex_") {
				continue
			}
			filtered = append(filtered, row)
		}
		rows = filtered
	} else {
		prefix := "worker_" + string(m.workerEditingTier)
		filtered := rows[:0]
		for _, row := range rows {
			if strings.HasPrefix(row.key, prefix) || row.key == "worker_save" || row.key == "worker_cancel" || row.key == "worker_reload" || row.key == "worker_current_bounds" {
				filtered = append(filtered, row)
			}
		}
		filtered = append(filtered, settingRow{key: "worker_back", name: "Back to Workers", kind: rowAction, run: func(m *settingsModel) tea.Cmd { m.backFromTierEdit(); return nil }})
		rows = filtered
	}
	return rows
}

func legacyWorkerBudgetLabel(seconds int) string {
	if seconds <= 0 {
		return "uncapped"
	}
	return fmt.Sprintf("%ds", seconds)
}

func workerCapabilityScope(a *config.WorkerAssignment) (string, string) {
	if a == nil {
		return "provider not assigned", "capability scope appears after choosing a provider"
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "ollama") {
		return "local request/tool/output quotas", "host runtime caps local scheduling; remote termination may remain unknown"
	}
	return "CLI request/tool/output quotas unavailable", "host runtime only; charge cap unavailable and nested CLI calls remain unknown"
}

func workerBillingRow(name config.WorkerTierName, rowName string, a *config.WorkerAssignment) settingRow {
	options := []string{"local", "subscription", "metered", "unknown"}
	current := "unknown"
	if a != nil {
		current = string(a.Billing)
	}
	return settingRow{key: "worker_" + string(name) + "_billing", name: rowName + " billing", kind: rowChoice, value: current, options: options, current: indexOf(options, current), set: func(m *settingsModel, i int) tea.Cmd {
		t := m.tierDraft(name)
		if t.Assignment == nil {
			m.message = "Choose a provider/model before editing billing"
			return nil
		}
		t.Assignment.Billing = config.WorkerBilling(options[i])
		return nil
	}}
}

func workerRef(a *config.WorkerAssignment, endpoint bool) string {
	if a == nil {
		return "not set"
	}
	v := a.AccountRef
	if endpoint {
		v = a.EndpointRef
	}
	if v == nil || strings.TrimSpace(*v) == "" {
		return "not set"
	}
	return *v
}

func workerBudget(v *int64) string {
	if v == nil {
		return "uncapped"
	}
	return fmt.Sprint(*v)
}

func workerIntPtr(v *int) string {
	if v == nil {
		return "uncapped"
	}
	return fmt.Sprint(*v)
}
