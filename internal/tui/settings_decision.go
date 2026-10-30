package tui

import (
	"context"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	tea "github.com/charmbracelet/bubbletea"
)

func decisionField(cfg *config.Config, key string) any {
	switch key {
	case "decision_enabled":
		return &cfg.DecisionEnabled
	case "decision_voice_work":
		return &cfg.DecisionVoiceWork
	case "decision_text_work":
		return &cfg.DecisionTextWork
	case "decision_provider":
		return &cfg.DecisionProvider
	case "decision_runtime":
		return &cfg.DecisionRuntime
	case "decision_device":
		return &cfg.DecisionDevice
	case "decision_local_model":
		return &cfg.DecisionLocalModel
	case "decision_endpoint":
		return &cfg.DecisionEndpoint
	case "decision_model":
		return &cfg.DecisionModel
	case "decision_api_key":
		return &cfg.DecisionAPIKey
	case "decision_budget_ms":
		return &cfg.DecisionBudgetMS
	case "decision_policy":
		return &cfg.DecisionPolicy
	}
	return nil
}

func (m settingsModel) decisionRows() []settingRow {
	keys := []string{"decision_enabled", "decision_voice_work", "decision_text_work", "decision_provider", "decision_runtime", "decision_local_model", "decision_device", "decision_endpoint", "decision_model", "decision_api_key", "decision_budget_ms", "decision_policy"}
	var rows []settingRow
	for _, key := range keys {
		spec, _ := config.SpecFor(key)
		row := settingRow{key: key, name: spec.Title, help: spec.Help, applies: appliesNextConversation}
		if m.onDecisionSettings != nil {
			row.applies = appliesNow
		}
		switch field := decisionField(m.cfg, key).(type) {
		case *bool:
			row.kind, row.value = rowToggle, onOff(*field)
			row.run = func(m *settingsModel) tea.Cmd { return m.saveDecision(key, !*decisionField(m.cfg, key).(*bool)) }
		case *string:
			row.value = *field
			if spec.Type == config.TypeEnum {
				row.kind, row.options = rowChoice, spec.Enum
				if row.value == "" {
					row.value = spec.Default.(string)
				}
				row.current = indexOf(row.options, row.value)
				row.set = func(m *settingsModel, i int) tea.Cmd { return m.saveDecision(key, spec.Enum[i]) }
				if key == "decision_local_model" {
					row.options = make([]string, len(spec.Enum))
					for i, id := range spec.Enum {
						model, _ := decider.ModelFor(id)
						row.options[i] = model.Name
					}
					if row.current >= 0 {
						row.value = row.options[row.current]
					}
				}
			} else {
				row.kind = rowText
				if spec.Secret {
					row.value = "not set"
					if *field != "" {
						row.value = "set"
					}
					row.help += " Empty uses DECISION_API_KEY. Stored keys are masked."
				}
				row.run = func(m *settingsModel) tea.Cmd {
					initial := *decisionField(m.cfg, key).(*string)
					kind := editDecision
					if spec.Secret {
						initial = ""
						kind = editDecisionKey
					}
					cmd := m.beginEdit(kind, spec.Title, initial)
					m.edit.decisionKey = key
					return cmd
				}
			}
		case *int:
			row.kind, row.value = rowText, strconv.Itoa(*field)
			row.run = func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editDecision, spec.Title, strconv.Itoa(*decisionField(m.cfg, key).(*int)))
				m.edit.decisionKey = key
				return cmd
			}
		}
		local := m.cfg.DecisionProvider == "" || m.cfg.DecisionProvider == "local"
		if (key == "decision_api_key" || key == "decision_model") && local {
			row.disabled = true
			row.reason = "for Jev or OpenAI"
		}
		if (key == "decision_runtime" || key == "decision_local_model" || key == "decision_device" || key == "decision_endpoint") && !local {
			row.disabled = true
			row.reason = "for local inference"
		}
		if key == "decision_endpoint" && m.cfg.DecisionRuntime != "external" {
			row.disabled = true
			row.reason = "external runtime only"
		}
		if (key == "decision_local_model" || key == "decision_device") && m.cfg.DecisionRuntime == "external" {
			row.disabled = true
			row.reason = "managed runtime only"
		}
		rows = append(rows, row)
		if key == "decision_local_model" {
			if model, err := decider.ModelFor(m.cfg.DecisionLocalModel); err == nil {
				details := model.Details()
				if _, err := decider.DeviceFor(runtime.GOOS, runtime.GOARCH, m.cfg.DecisionDevice, false); err != nil {
					details += " · " + err.Error()
				}
				rows = append(rows, settingRow{key: "decision_model_details", name: model.Name, kind: rowInfo, value: details, help: "Model weights and code are pinned. Memory is an estimate, not a hardware guarantee. Liquid license: https://huggingface.co/LiquidAI/d1-3B/blob/main/LICENSE"})
			}
		}
	}
	status := "off"
	if m.cfg.DecisionEnabled {
		status = "starts with the next conversation"
	}
	if m.decisionStatus != nil {
		current := m.decisionStatus()
		status = current.Phase
		if current.Detail != "" {
			status += " · " + current.Detail
		}
		if current.Err != nil {
			status += " · " + current.Err.Error()
		}
	}
	rows = append(rows, settingRow{key: "decision_test", name: "Test on this machine", kind: rowAction, verb: "test", disabled: m.testDecisions == nil || m.decisionTestRunning || !config.ManagedDecisions(m.cfg) || status == "off" || (m.decisionStatus != nil && m.decisionStatus().Phase != "ready"), reason: "requires a ready managed runtime", help: "Runs one synthetic choice without conversation data or tools. Compares warm latency with your deadline; does not measure routing accuracy.", run: func(m *settingsModel) tea.Cmd {
		m.decisionTestRunning = true
		m.decisionTestEpoch = new(int)
		m.message = "Testing the selected local model…"
		epoch := m.decisionTestEpoch
		run := m.testDecisions
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			result, err := run(ctx)
			return decisionTestMsg{epoch: epoch, result: result, err: err}
		}
	}})
	return append(rows, settingRow{key: "decision_status", name: "Decision runtime", kind: rowInfo, value: status, help: "Experimental shortcuts require enabled worker tiers. Late, uncertain or unavailable decisions use the ordinary brain."})
}

func (m *settingsModel) saveDecision(key string, value any) tea.Cmd {
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save(key, value); err != nil {
		m.message = "Decision setting was not saved: " + err.Error()
		return nil
	}
	switch field := decisionField(m.cfg, key).(type) {
	case *bool:
		*field = value.(bool)
	case *string:
		*field = value.(string)
	case *int:
		*field = value.(int)
	}
	m.decisionTestEpoch = nil
	m.decisionTestRunning = false
	m.message = "Decision setting saved; applies to the next conversation"
	if m.onDecisionSettings != nil {
		m.onDecisionSettings(m.cfg.Clone())
		m.message = "Decision setting applied"
	}
	return m.pollDecisionStatus()
}

func (m *settingsModel) commitDecisionEdit(key, value string) tea.Cmd {
	if key == "decision_budget_ms" {
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 25 || n > 3000 {
			m.message = "Decision deadline must be 25–3000 ms"
			return nil
		}
		return m.saveDecision(key, n)
	}
	if _, ok := decisionField(m.cfg, key).(*string); !ok {
		m.message = fmt.Sprintf("Unknown decision field %q", key)
		return nil
	}
	return m.saveDecision(key, strings.TrimSpace(value))
}

type decisionTestMsg struct {
	epoch  *int
	result decision.SmokeResult
	err    error
}

type decisionStatusTickMsg struct{}

func (m settingsModel) pollDecisionStatus() tea.Cmd {
	if m.decisionStatus == nil || m.decisionStatus().Phase != "preparing" {
		return nil
	}
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return decisionStatusTickMsg{} })
}
