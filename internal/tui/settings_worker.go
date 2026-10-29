package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

// workerSettingsSavedMsg is consumed by the root TUI to refresh the runtime
// worker resolver after the complete object has been committed.
type workerSettingsSavedMsg struct{ workers *config.VoiceWorkers }

func (m *settingsModel) ensureWorkerDraft() *config.VoiceWorkers {
	if m.workerDraft != nil {
		return m.workerDraft
	}
	if m.cfg.VoiceWorkers != nil {
		m.workerDraft = m.cfg.VoiceWorkers.Clone()
		m.workerOpenRevision = m.cfg.VoiceWorkers.Revision
	} else {
		m.workerDraft = config.DefaultVoiceWorkers()
		m.workerOpenRevision = 0
	}
	return m.workerDraft
}

func (m *settingsModel) previewWorkerMigration() tea.Cmd {
	effective := m.cfg
	if m.workerMigrationConfig != nil {
		if candidate := m.workerMigrationConfig(); candidate != nil {
			effective = candidate
		}
	}
	draft, err := config.PreviewLegacyWorkerMigration(effective)
	if err != nil {
		m.message = "Worker migration preview failed: " + err.Error()
		return nil
	}
	m.workerDraft = draft
	if m.cfg.VoiceWorkers != nil {
		m.workerOpenRevision = m.cfg.VoiceWorkers.Revision
	}
	// Preserve policy controls already configured by the installation.
	if m.cfg.VoiceWorkers != nil {
		draft.Policy = m.cfg.VoiceWorkers.Clone().Policy
	}
	m.message = "Tiered draft ready — choose billing for each enabled tier, then Save"
	return nil
}

func (m *settingsModel) tierDraft(name config.WorkerTierName) *config.WorkerTier {
	d := m.ensureWorkerDraft()
	switch name {
	case config.WorkerTierSimple:
		return &d.Tiers.Simple
	case config.WorkerTierMedium:
		return &d.Tiers.Medium
	case config.WorkerTierComplex:
		return &d.Tiers.Complex
	}
	return nil
}

func (m *settingsModel) beginTierEdit(name config.WorkerTierName) tea.Cmd {
	m.workerEditingTier = name
	t := m.tierDraft(name)
	if t == nil {
		return nil
	}
	initial := ""
	if t.Assignment != nil {
		initial = t.Assignment.Provider + "/" + t.Assignment.Model
	}
	cmd := m.beginEdit(editWorkerAssignment, workerTierTitle(string(name))+" provider/model", initial)
	m.edit.workerTier = string(name)
	return cmd
}

func (m *settingsModel) backFromTierEdit() { m.workerEditingTier = ""; m.message = "Worker tier list" }

func (m *settingsModel) commitWorkerAssignment(tier, value string) {
	parts := strings.SplitN(strings.TrimSpace(value), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		m.message = "Worker assignment must be provider/model"
		return
	}
	provider, model := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	found := false
	for _, p := range m.workerProviderInfos() {
		if strings.EqualFold(p.Name, provider) {
			if !p.Available {
				m.message = provider + " is not available on this machine"
				return
			}
			for _, candidate := range p.Models {
				if candidate == model {
					found = true
					break
				}
			}
			if len(p.Models) == 0 && model == "default" {
				found = true
			}
			break
		}
	}
	if !found {
		m.message = "Worker provider/model is not available: " + provider + "/" + model
		return
	}
	name := config.WorkerTierName(tier)
	t := m.tierDraft(name)
	billing := config.WorkerBillingUnknown
	if strings.EqualFold(provider, "ollama") {
		billing = config.WorkerBillingLocal
	}
	assignment := config.WorkerAssignment{Billing: config.WorkerBillingUnknown}
	if t.Assignment != nil {
		assignment = *t.Assignment
	}
	if t.Assignment == nil {
		assignment.Billing = billing
		if strings.EqualFold(provider, "ollama") {
			host := m.cfg.VoiceWorkerOllamaHost
			if host == "" {
				host = m.cfg.OllamaHost
			}
			if host != "" {
				assignment.EndpointRef = &host
			}
		}
	}
	assignment.Provider, assignment.Model = provider, model
	t.Assignment = &assignment
	m.message = workerTierTitle(string(name)) + " draft updated"
}

func (m *settingsModel) beginWorkerReferenceEdit(name config.WorkerTierName, field, initial string) tea.Cmd {
	if initial == "not set" {
		initial = ""
	}
	cmd := m.beginEdit(editWorkerReference, field, initial)
	m.edit.workerTier = string(name)
	return cmd
}

func (m *settingsModel) commitWorkerReference(tier, field, value string) {
	t := m.tierDraft(config.WorkerTierName(tier))
	if t.Assignment == nil {
		m.message = "Choose a provider/model before editing references"
		return
	}
	v := strings.TrimSpace(value)
	var dst **string
	if strings.Contains(strings.ToLower(field), "endpoint") {
		dst = &t.Assignment.EndpointRef
	} else {
		dst = &t.Assignment.AccountRef
	}
	if v == "" {
		*dst = nil
	} else {
		*dst = &v
	}
	m.message = workerTierTitle(tier) + " " + field + " draft updated"
}

func (m *settingsModel) beginWorkerLimitEdit(name config.WorkerTierName, field string, initial int) tea.Cmd {
	cmd := m.beginEdit(editWorkerLimit, workerTierTitle(string(name))+" "+field, strconv.Itoa(initial))
	m.edit.workerTier, m.edit.workerField = string(name), field
	return cmd
}

func (m *settingsModel) beginWorkerLimitTextEdit(name config.WorkerTierName, field, initial string) tea.Cmd {
	cmd := m.beginEdit(editWorkerLimit, workerTierTitle(string(name))+" "+field, initial)
	m.edit.workerTier, m.edit.workerField = string(name), field
	return cmd
}

func (m *settingsModel) commitWorkerLimit(tier, field, value string) {
	if (field == "charge_micro_usd" || field == "output_tokens") && (strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "uncapped")) {
		t := m.tierDraft(config.WorkerTierName(tier))
		if field == "output_tokens" {
			t.Limits.OutputTokens = nil
		} else {
			t.Limits.ChargeMicroUSD = nil
		}
		m.message = workerTierTitle(tier) + " " + field + " draft updated"
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 || (field != "transport_retries" && n < 1) {
		m.message = "Worker limit must be a positive integer"
		return
	}
	t := m.tierDraft(config.WorkerTierName(tier))
	switch field {
	case "runtime_seconds":
		t.Limits.RuntimeSeconds = n
	case "model_requests":
		t.Limits.ModelRequests = n
	case "tool_calls":
		t.Limits.ToolCalls = n
	case "transport_retries":
		t.Limits.TransportRetries = n
	case "output_tokens":
		t.Limits.OutputTokens = &n
	case "charge_micro_usd":
		v := int64(n)
		t.Limits.ChargeMicroUSD = &v
	default:
		m.message = "Unknown worker limit"
		return
	}
	m.message = workerTierTitle(tier) + " " + field + " draft updated"
}

func (m *settingsModel) beginWorkerPolicyEdit(field, initial string) tea.Cmd {
	cmd := m.beginEdit(editWorkerPolicy, field, initial)
	m.edit.workerField = field
	return cmd
}

func (m *settingsModel) commitWorkerPolicy(field, value string) {
	v := strings.TrimSpace(value)
	switch field {
	case "timezone":
		if v == "" {
			m.message = "Worker timezone is required"
			return
		}
		m.ensureWorkerDraft().Policy.Timezone = v
	case "queued_workers":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 32 {
			m.message = "Queued workers must be 1–32"
			return
		}
		m.ensureWorkerDraft().Policy.QueuedWorkers = n
	case "daily_charge_micro_usd":
		if v == "" || strings.EqualFold(v, "uncapped") {
			m.ensureWorkerDraft().Policy.DailyChargeMicroUSD = nil
			break
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			m.message = "Daily budget must be a positive integer"
			return
		}
		m.ensureWorkerDraft().Policy.DailyChargeMicroUSD = &n
	default:
		m.message = "Unknown worker policy"
		return
	}
	m.message = "Worker policy draft updated"
}

func (m *settingsModel) saveWorkerDraft() tea.Cmd {
	if m.workerDraft == nil {
		m.message = "No worker changes to save"
		return nil
	}
	draft := m.workerDraft.Clone()
	draft.Mode = config.WorkerModeTiered
	if err := draft.Validate(); err != nil {
		m.message = "Worker settings invalid: " + err.Error()
		return nil
	}
	for _, name := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex} {
		t := draft.Tiers.Simple
		switch name {
		case config.WorkerTierMedium:
			t = draft.Tiers.Medium
		case config.WorkerTierComplex:
			t = draft.Tiers.Complex
		}
		if !t.Enabled {
			continue
		}
		if t.Assignment == nil {
			m.message = "Cannot save " + workerTierTitle(string(name)) + ": enabled tier has no assignment"
			return nil
		}
		if err := (brain.LocalWorkerCapabilities{}).ValidateWorkerAssignment(*t.Assignment); err != nil {
			m.message = "Cannot save " + workerTierTitle(string(name)) + ": " + err.Error()
			return nil
		}
	}
	save := m.saveWorkers
	if save == nil {
		save = config.CommitVoiceWorkersCAS
	}
	saved, err := save(draft, m.workerOpenRevision)
	if err != nil {
		m.message = "Failed to save worker settings: " + err.Error()
		if errors.Is(err, config.ErrWorkerRevisionConflict) {
			m.message += "; draft kept. Use Reload saved worker settings to discard edits and load the latest revision."
		}
		return nil
	}
	if saved == nil {
		m.message = "Worker save returned no committed configuration"
		return nil
	}
	m.cfg.VoiceWorkers = saved.Clone()
	m.workerDraft = saved.Clone()
	m.workerOpenRevision = saved.Revision
	m.message = "Worker settings saved"
	return func() tea.Msg { return workerSettingsSavedMsg{workers: saved.Clone()} }
}

// Disabled tiers may retain incomplete setup without blocking other assignments.
func workerTierUnavailable(w *config.VoiceWorkers, name config.WorkerTierName) string {
	t := w.Tiers.Simple
	switch name {
	case config.WorkerTierMedium:
		t = w.Tiers.Medium
	case config.WorkerTierComplex:
		t = w.Tiers.Complex
	}
	if !t.Enabled {
		return ""
	}
	_, err := config.ResolveWorkerPlan(&config.Config{VoiceWorkers: w}, name, brain.LocalWorkerCapabilities{})
	if err != nil {
		return err.Error()
	}
	return ""
}

func (m *settingsModel) reloadWorkerDraft() tea.Cmd {
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		m.message = "Cannot reload worker settings: " + err.Error()
		return nil
	}
	m.cfg.VoiceWorkers = loaded.VoiceWorkers.Clone()
	m.workerDraft = nil
	m.workerOpenRevision = 0
	if loaded.VoiceWorkers != nil {
		m.workerOpenRevision = loaded.VoiceWorkers.Revision
	}
	m.workerEditingTier = ""
	m.message = "Saved worker settings reloaded; draft edits discarded"
	return nil
}

func (m *settingsModel) cancelWorkerDraft() {
	m.workerDraft = nil
	if m.cfg.VoiceWorkers != nil {
		m.workerOpenRevision = m.cfg.VoiceWorkers.Revision
	}
	m.message = "Worker edits cancelled"
}

// The worker is the second brain: it runs the background job a spawn starts,
// with full tools and no one watching. Picking it is part of picking a brain,
// not a buried key — an unchosen worker used to fall through to a remote
// default nobody selected, and then acted on whatever it found on disk.
//
// Provider and model share one section, like the brain's: they are chosen
// together.

const (
	workerRowProvider = iota
	workerRowModel
	workerRowDivider
)

// workerItem is one row of the Worker section: a selectable provider, a
// selectable model for the chosen provider, or the rule between them.
type workerItem struct {
	kind  int
	value string
	label string
}

// workerProviderInfos is the brain provider list narrowed to backends that can
// run async jobs, so availability and model catalogs come from one discovery.
func (m settingsModel) workerProviderInfos() []discovery.ProviderInfo {
	var out []discovery.ProviderInfo
	for _, p := range m.providers {
		if config.WorkerProviderAllowed(p.Name) {
			out = append(out, p.ForWorkers())
		}
	}
	return out
}

func (m *settingsModel) buildWorkerItems() {
	m.workerItems = nil
	for _, p := range m.workerProviderInfos() {
		label := p.Name
		if !p.Available {
			label += " (not available)"
		}
		m.workerItems = append(m.workerItems, workerItem{kind: workerRowProvider, value: p.Name, label: label})
	}
	m.workerItems = append(m.workerItems, workerItem{kind: workerRowDivider, label: "Worker model"})
	for _, model := range m.workerModelChoices() {
		m.workerItems = append(m.workerItems, workerItem{kind: workerRowModel, value: model, label: "  " + model})
	}
}

// workerModelChoices are the models the chosen worker backend offers, always
// led by "default" so the pin can be cleared.
func (m settingsModel) workerModelChoices() []string {
	out := []string{"default"}
	active := config.VoiceWorkerProviderOf(m.cfg)
	for _, p := range m.workerProviderInfos() {
		if strings.EqualFold(p.Name, active) {
			for _, model := range p.Models {
				if model != "default" {
					out = append(out, model)
				}
			}
			break
		}
	}
	return out
}

// activeWorkerProvider is the provider the Worker rows tick, or "" when the
// operator has not chosen one. It deliberately does not report the inherited
// fallback: an unselected worker must read as unselected.
func (m settingsModel) activeWorkerProvider() string {
	if !config.WorkerSelected(m.cfg) {
		return ""
	}
	return config.VoiceWorkerProviderOf(m.cfg)
}

// activeWorkerModel is the pinned worker model, or "default" when empty.
func (m settingsModel) activeWorkerModel() string {
	if model := strings.TrimSpace(m.cfg.VoiceWorkerModel); model != "" {
		return model
	}
	return "default"
}

// workerStatus is the one-line summary the Worker section shows. It names the
// inherited provider when nothing was chosen so the fallback is visible rather
// than silent.
func (m settingsModel) workerStatus() string {
	if !config.WorkerSelected(m.cfg) {
		return fmt.Sprintf("Worker not set — inheriting %s until you choose", config.VoiceWorkerProviderOf(m.cfg))
	}
	return fmt.Sprintf("Worker: %s · %s", config.VoiceWorkerProviderOf(m.cfg), m.activeWorkerModel())
}

// setWorkerProvider saves the worker backend. Like the brain rows, the live
// config is mutated only after the save succeeds.
func (m *settingsModel) setWorkerProvider(name string) {
	if !m.workerProviderAvailable(name) {
		m.message = fmt.Sprintf("%s is not available on this machine", name)
		return
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("voice_worker_provider", name); err != nil {
		m.message = fmt.Sprintf("Failed to save worker: %v", err)
		return
	}
	m.cfg.VoiceWorkerProvider = name
	// A model pinned for the old backend is meaningless for the new one.
	if strings.TrimSpace(m.cfg.VoiceWorkerModel) != "" {
		if err := save("voice_worker_model", ""); err == nil {
			m.cfg.VoiceWorkerModel = ""
		}
	}
	m.buildWorkerItems()
	m.message = fmt.Sprintf("Worker set to %s", name)
}

// setWorkerModel pins a model for the chosen worker; "default" clears the pin.
func (m *settingsModel) setWorkerModel(model string) {
	if model != "default" && indexOf(m.workerModelChoices(), model) < 0 {
		m.message = "Worker model is unavailable or lacks verified tool support: " + model
		return
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	value := model
	if value == "default" {
		value = ""
	}
	if err := save("voice_worker_model", value); err != nil {
		m.message = fmt.Sprintf("Failed to save worker model: %v", err)
		return
	}
	m.cfg.VoiceWorkerModel = value
	m.message = fmt.Sprintf("Worker model set to %s", model)
}

func (m settingsModel) workerProviderAvailable(name string) bool {
	for _, p := range m.workerProviderInfos() {
		if strings.EqualFold(p.Name, name) {
			return p.Available
		}
	}
	return false
}

func workerTierTitle(tier string) string {
	switch tier {
	case "simple":
		return "Simple"
	case "medium":
		return "Medium"
	case "complex":
		return "Complex"
	default:
		return tier
	}
}
