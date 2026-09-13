package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// A row is a setting. Every section is a list of settingRow values built from
// the live config each time it is drawn. A row that chooses from a list draws
// the whole list under a heading with ✓ on the current item, because seeing
// the options is what a settings screen is for; the other kinds draw as one
// line, name then value.
//
// The kind is the verb:
//
//	rowPick    one of a list — the options are drawn; enter on one chooses it
//	rowChoice  one of a few — drawn on the row with [current] marked; enter,
//	           space or +/- move to the next
//	rowToggle  on or off — enter or space flips it
//	rowSlider  a level — +/- nudge it
//	rowText    typed — enter opens the one-line editor
//	rowAction  does something — enter runs it
//	rowMenu    things to act on — drawn like a list; enter on one acts
//	rowInfo    read-only; enter may explain
type rowKind int

const (
	rowPick rowKind = iota
	rowChoice
	rowToggle
	rowSlider
	rowText
	rowAction
	rowMenu
	rowInfo
)

// When a change takes effect, stated on the row as a fact.
const (
	appliesNow              = "applies immediately"
	appliesNextConversation = "applies to the next conversation"
	appliesOnReturn         = "applies when you return to the conversation"
	appliesNextMeeting      = "applies to the next meeting"
	appliesUnlessPersona    = "applies immediately unless the persona has its own voice"
)

type settingRow struct {
	// key is the config key the row writes; "" for actions and info rows.
	key  string
	name string
	kind rowKind
	// value is the current value as the row shows it; detail is a dim tail.
	value  string
	detail string
	// options and current are the list behind a pick, choice or menu row.
	// current is -1 when nothing in the list is the current value.
	options []string
	current int
	// A disabled row draws dim with its reason and ignores every verb.
	disabled bool
	reason   string
	applies  string
	help     string
	// verb names what enter does on an action or menu row, for the footer.
	verb string
	// muted marks options drawn dim (index-aligned with options; nil means
	// none): a backend that is not available, an engine or tier whose choice
	// starts a download. They are still listed and can still be chosen; the
	// row's help says what choosing one does.
	muted []bool
	// set applies option idx (pick, choice, menu). run performs the row's
	// own verb (toggle, text, action, info). nudge moves a slider by delta
	// steps. preview plays option idx without choosing it. Each takes the
	// model explicitly: a closure built on one copy must not mutate a stale
	// one.
	set     func(m *settingsModel, idx int) tea.Cmd
	run     func(m *settingsModel) tea.Cmd
	nudge   func(m *settingsModel, delta int) tea.Cmd
	preview func(m *settingsModel, idx int) tea.Cmd
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func indexOf(options []string, value string) int {
	for i, o := range options {
		if strings.EqualFold(o, value) {
			return i
		}
	}
	return -1
}

// sectionRows is the current section as rows. A section is what you would
// change together: the brain and its model, the voice engine and its voices,
// the microphone and the speaker.
func (m settingsModel) sectionRows() []settingRow {
	switch m.section {
	case sectionBrain:
		return append(m.providerRows(), m.modelRows()...)
	case sectionWorker:
		return m.workerRows()
	case sectionTools:
		return m.toolRows()
	case sectionVoice:
		rows := append(m.ttsRows(), m.voiceRows()...)
		return append(rows, m.languageRows()...)
	case sectionQwen:
		return m.qwenRows()
	case sectionSTT:
		return m.sttRows()
	case sectionSpeakers:
		return m.speakerSettingRows()
	case sectionAudio:
		return append(m.inputRows(), m.outputRows()...)
	case sectionMeeting:
		return m.meetingRows()
	}
	return nil
}

// ── the rows, section by section ─────────────────────────────────────

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
		help:    "Pin a model for the worker, or leave it on the provider's default.",
		set: func(m *settingsModel, i int) tea.Cmd {
			m.setWorkerModel(models[i])
			return nil
		},
	}
	if config.WorkerSelected(m.cfg) {
		model.current = indexOf(models, m.activeWorkerModel())
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
	rows = append(rows, permission)
	rows = append(rows, settingRow{key: "worker_effective_bounds", name: "Effective worker limits", kind: rowInfo,
		value:  fmt.Sprintf("legacy: %d concurrent · %s/job", max(1, m.cfg.VoiceWorkerMaxConcurrent), legacyWorkerBudgetLabel(m.cfg.VoiceWorkerJobBudget)),
		detail: "legacy singleton controls; tier policy is not active",
		help:   "These singleton settings control jobs until an explicit tiered worker policy is saved."})
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

func (m settingsModel) toolRows() []settingRow {
	cfg := m.cfg
	toggle := func(row int) func(*settingsModel) tea.Cmd {
		return func(m *settingsModel) tea.Cmd { return m.selectToolRow(row) }
	}
	skills := settingRow{
		key: "skills_enabled", name: "Agent Skills", kind: rowToggle,
		value: onOff(cfg.SkillsEnabled), applies: appliesNextConversation,
		help: "Let the brain load SKILL.md skills from the skills directory.",
		run:  toggle(toolRowSkills),
	}
	if !strings.EqualFold(cfg.BrainProvider, "ollama") {
		skills.disabled = true
		skills.reason = "Ollama only"
	}
	modes := talkhotkey.Modes()
	mode := string(config.TalkHotkeyModeOf(cfg))
	return []settingRow{
		{
			key: "voice_tools_enabled", name: "Local tools", kind: rowToggle,
			value: onOff(cfg.VoiceToolsEnabled), applies: appliesNextConversation,
			help: "Let the brain run local tools during a conversation.",
			run:  toggle(toolRowLocalTools),
		},
		skills,
		{
			key: "barge_in_enabled", name: "Voice barge-in", kind: rowToggle,
			value: onOff(cfg.BargeInEnabled), applies: appliesNextConversation,
			help: "Interrupt it by talking over it. Turn on the voice front end too, or it hears itself.",
			run:  toggle(toolRowBargeIn),
		},
		{
			key: "name_summon_enabled", name: "Name summon", kind: rowToggle,
			value: onOff(cfg.NameSummonEnabled), applies: appliesNextConversation,
			help: "Wake it by saying its name. Needs the voice front end so playback cannot summon it.",
			run:  toggle(toolRowNameSummon),
		},
		{
			key: "voice_frontend_enabled", name: "Voice front end (AEC)", kind: rowToggle,
			value: onOff(cfg.VoiceFrontendEnabled), applies: appliesNextConversation,
			help: "Echo cancellation and noise suppression on the microphone.",
			run:  toggle(toolRowVoiceFrontend),
		},
		{
			key: "tui_mouse_enabled", name: "Mouse scroll", kind: rowToggle,
			value: onOff(cfg.TUIMouseEnabled), applies: "applies when you return to chat",
			help: "The wheel scrolls the chat; hold option, fn or shift to select text.",
			run:  toggle(toolRowMouse),
		},
		{
			key: "talk_hotkey_enabled", name: "Talk key", kind: rowToggle,
			value: onOff(cfg.TalkHotkeyEnabled), applies: appliesNextConversation,
			help: "A system-wide key that opens the microphone.",
			run:  toggle(toolRowTalkKey),
		},
		{
			key: "talk_hotkey_mode", name: "Talk key mode", kind: rowChoice,
			value: mode, options: modes, current: indexOf(modes, mode),
			applies: appliesNextConversation,
			help:    "hold: press to talk, release to send · toggle: tap to start, tap to send · automatic: both.",
			set:     func(m *settingsModel, i int) tea.Cmd { return m.setTalkMode(modes[i]) },
		},
		{
			key: "talk_hotkey", name: "Talk key combo", kind: rowText,
			value: m.talkComboLabel(), applies: appliesNextConversation,
			help: "Type the combination — ctrl+alt+t, f13. Empty means the platform default.",
			run:  func(m *settingsModel) tea.Cmd { return m.beginTalkComboEdit() },
		},
	}
}

func (m settingsModel) ttsRows() []settingRow {
	active := activeTTSProvider(m.cfg)
	items := m.ttsItems
	options := make([]string, len(items))
	muted := make([]bool, len(items))
	current := -1
	for i, it := range items {
		options[i] = it.provider + " — " + it.detail
		if it.provider == active {
			current = i
		}
		// Choosing the managed Qwen engine before its package is on disk
		// starts a large download; it is drawn dim so that is chosen
		// knowingly, and the help says so.
		muted[i] = m.ttsProviderInstalls(it.provider)
	}
	return []settingRow{{
		key: "tts_provider", name: "Engine", kind: rowPick,
		value: active, options: options, current: current, muted: muted,
		applies: appliesUnlessPersona,
		help:    "Which engine makes the sound. Choosing an uninstalled one from the list installs it.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.selectTTSProvider(items[i].provider) },
	}}
}

func (m settingsModel) qwenRows() []settingRow {
	tiers := []string{managedqwen.DefaultModelTier, managedqwen.Tier1_7B}
	tier := strings.TrimSpace(m.cfg.QwenTTSModelTier)
	if tier == "" {
		tier = managedqwen.DefaultModelTier
	}
	places := []string{"This computer", "Another computer"}
	place := 0
	if config.TTSPlaceOf(m.cfg) == config.TTSPlaceLAN {
		place = 1
	}
	rows := make([]settingRow, 0, len(m.qwenItems))
	for _, it := range m.qwenItems {
		kind := it.kind
		row := settingRow{name: it.name, value: it.value, detail: it.detail}
		switch kind {
		case qwenOptStatus:
			row.kind = rowInfo
			row.verb = "details"
			row.help = "The native Qwen3-TTS package on this computer."
			row.run = func(m *settingsModel) tea.Cmd { return m.selectQwenKind(qwenOptStatus) }
		case qwenOptPlace:
			row.key, row.kind = "tts_place", rowChoice
			row.options, row.current = places, place
			row.applies = appliesOnReturn
			row.help = "Where speech is synthesized: here, or a speech worker on the network."
			row.set = func(m *settingsModel, i int) tea.Cmd { return m.setPlace(i == 1) }
		case qwenOptWorkerURL:
			row.key, row.kind = "tts_worker_url", rowText
			row.applies = appliesOnReturn
			row.help = "Address of the speech worker, e.g. http://studio.local:8787."
			row.run = func(m *settingsModel) tea.Cmd {
				return m.beginEdit(editWorkerURL, "Speech computer address", strings.TrimSpace(m.cfg.TTSWorkerURL))
			}
		case qwenOptWorkerToken:
			row.key, row.kind = "tts_worker_token", rowText
			row.applies = appliesOnReturn
			row.help = "Shared secret for the speech worker. Typed here, never shown."
			row.run = func(m *settingsModel) tea.Cmd {
				return m.beginEdit(editWorkerToken, "Speech computer token", "")
			}
		case qwenOptLanStrict:
			row.key, row.kind = "tts_lan_strict", rowToggle
			row.applies = appliesNextConversation
			row.help = "Refuse to start without the speech computer, instead of falling back to Kokoro here."
			row.run = func(m *settingsModel) tea.Cmd { return m.toggleLanStrict() }
		case qwenOptTier:
			row.key, row.kind = "qwen_tts_model_tier", rowPick
			row.options, row.current = tiers, indexOf(tiers, tier)
			row.muted = make([]bool, len(tiers))
			for i, t := range tiers {
				// setQwenTier fetches a tier that is not in the package; it
				// is drawn dim so the download is chosen knowingly.
				row.muted[i] = t == managedqwen.Tier1_7B && !slices.Contains(m.nativeStatus.TiersReady, t)
			}
			row.applies = appliesNextConversation
			row.help = "0.6b is fast; 1.7b sounds better and is slower. A tier not in the package is downloaded."
			row.set = func(m *settingsModel, i int) tea.Cmd { return m.setQwenTier(tiers[i]) }
		case qwenOptConsent:
			row.key, row.kind = "qwen_tts_consent", rowToggle
			row.applies = appliesNow
			row.help = "Allow cloning a reference voice."
			row.run = func(m *settingsModel) tea.Cmd { return m.selectQwenKind(qwenOptConsent) }
		case qwenOptCache:
			row.kind, row.verb = rowAction, "clear"
			row.help = "Cached voice embeddings. Clearing is safe; they are rebuilt on use."
			row.run = func(m *settingsModel) tea.Cmd { return m.selectQwenKind(qwenOptCache) }
		case qwenOptInstall:
			row.kind, row.verb = rowAction, "install"
			row.help = "Downloads the native package — a large download. Nothing else changes until it finishes."
			row.run = func(m *settingsModel) tea.Cmd { return m.selectQwenKind(qwenOptInstall) }
		}
		rows = append(rows, row)
	}
	return rows
}

func voiceOptionLabel(v tts.Voice) string {
	return fmt.Sprintf("%-16s %s / %s", v.Name, v.Gender, v.Locale)
}

func (m settingsModel) voiceRows() []settingRow {
	provider := activeTTSProvider(m.cfg)
	row := settingRow{
		key: config.TTSVoiceKeyFor(provider), name: "Voice", kind: rowPick,
		applies: appliesUnlessPersona,
		help:    "Which voice speaks. Personas with their own voice keep it.",
	}
	if len(m.voiceItems) == 0 {
		row.disabled = true
		row.reason = ttsVoiceSelectionStatus(m.cfg)
		return []settingRow{row}
	}
	active := config.TTSVoiceFor(m.cfg, provider)
	// The closures act on the list this row showed, not on whatever the
	// model holds when the key lands: a Qwen install finishing while the
	// picker is open rebuilds voiceItems underneath it.
	voices := m.voiceItems
	options := make([]string, len(voices))
	current := -1
	for i, v := range voices {
		options[i] = voiceOptionLabel(v)
		if strings.EqualFold(v.Name, active) {
			current = i
		}
	}
	row.options, row.current = options, current
	row.value = active
	if current >= 0 {
		row.value = voices[current].Name
	}
	if m.preview.previewing != "" {
		row.detail = "♫ playing " + m.preview.previewing + "…"
	}
	row.set = func(m *settingsModel, i int) tea.Cmd { return m.selectVoice(voices[i]) }
	row.preview = func(m *settingsModel, i int) tea.Cmd {
		if i < 0 || i >= len(voices) {
			return nil
		}
		return m.startSettingsPreview(voices[i])
	}
	return []settingRow{row}
}

func (m settingsModel) languageRows() []settingRow {
	row := settingRow{
		key: "qwen_tts_language", name: "Language", kind: rowPick,
		applies: appliesOnReturn,
		help:    "Which language Qwen3-TTS speaks. Auto follows the text.",
	}
	if len(m.languageItems) == 0 {
		row.disabled = true
		row.reason = "Language selection is available for managed Qwen3-TTS"
		return []settingRow{row}
	}
	languages := m.languageItems
	current := -1
	for i, language := range languages {
		if strings.EqualFold(language, m.cfg.QwenTTSLanguage) ||
			(m.cfg.QwenTTSLanguage == "" && language == "Auto") {
			current = i
		}
	}
	row.options, row.current = languages, current
	row.value = m.cfg.QwenTTSLanguage
	if current >= 0 {
		row.value = languages[current]
	}
	row.set = func(m *settingsModel, i int) tea.Cmd { return m.selectLanguage(languages[i]) }
	return []settingRow{row}
}

func (m settingsModel) sttRows() []settingRow {
	active := activeSTTProvider(m.cfg)
	items := m.sttItems
	options := make([]string, len(items))
	current := -1
	for i, it := range items {
		options[i] = it.label
		if it.provider == active {
			current = i
		}
	}
	value := active
	if value == "" {
		value = "not set"
	}
	return []settingRow{{
		key: "stt_provider", name: "Recognizer", kind: rowPick,
		value: value, detail: sttConfiguredAliasNote(m.cfg), options: options, current: current,
		applies: "applies to a new conversation",
		help:    "Which speech recognizer hears you.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.selectSTTItem(items[i]) },
	}}
}

func (m settingsModel) speakerSettingRows() []settingRow {
	thresholds := make([]string, len(liveThresholdChoices))
	for i, t := range liveThresholdChoices {
		thresholds[i] = fmt.Sprintf("%.2f", t)
	}
	windows := make([]string, len(liveWindowChoices))
	for i, w := range liveWindowChoices {
		windows[i] = fmt.Sprintf("%d ms", w)
	}
	sp := m.cfg.Speaker
	var rows []settingRow
	for _, r := range m.speakerRows() {
		r := r
		run := func(m *settingsModel) tea.Cmd { return m.selectSpeakerRow(r) }
		var row settingRow
		switch r.kind {
		case speakerRowName:
			name := strings.TrimSpace(m.cfg.UserDisplayName)
			if name == "" {
				name = "not set"
			}
			row = settingRow{
				key: "user_display_name", name: "Your name", kind: rowText, value: name,
				applies: appliesNextConversation,
				help:    "Used instead of speaker-1 in chat labels and transcripts.",
				run:     run,
			}
		case speakerRowLive:
			row = settingRow{
				key: "speaker.live.enabled", name: "Speaker labels in chat", kind: rowToggle,
				value: onOff(sp.Enabled && sp.Live.Enabled), applies: appliesNextConversation,
				help: "Label who is talking in a conversation.",
				run:  run,
			}
		case speakerRowThreshold:
			current := m.effectiveLiveThreshold()
			row = settingRow{
				key: "speaker.live.threshold", name: "Match threshold", kind: rowChoice,
				value: fmt.Sprintf("%.2f", current), options: thresholds, current: nearestIndex32(liveThresholdChoices, current),
				applies: appliesNextConversation,
				help:    "Lower merges similar voices onto one label; higher splits them.",
				set: func(m *settingsModel, i int) tea.Cmd {
					m.setLiveThreshold(liveThresholdChoices[i])
					return nil
				},
			}
		case speakerRowWindow:
			window := sp.Live.WindowMS
			if window <= 0 {
				window = 1500
			}
			row = settingRow{
				key: "speaker.live.window_ms", name: "Analysis window", kind: rowChoice,
				value: fmt.Sprintf("%d ms", window), options: windows, current: indexOf(windows, fmt.Sprintf("%d ms", window)),
				applies: appliesNextConversation,
				help:    "Shorter labels a turn sooner; longer gives the embedder more voice. Enrolled voices go stale.",
				set: func(m *settingsModel, i int) tea.Cmd {
					m.setLiveWindow(liveWindowChoices[i])
					return nil
				},
			}
		case speakerRowMeeting:
			row = settingRow{
				name: "Meeting diarization", kind: rowInfo,
				value: onOff(sp.Enabled && sp.Meeting.Enabled), detail: "set in the Meeting tab",
				verb: "where", help: "Per-meeting; toggle it under Meeting.",
				run: run,
			}
		case speakerRowRegister:
			row = settingRow{
				name: "Register your voice", kind: rowAction, verb: "record",
				help: "Records three short clips so live labels know your voice. The microphone is live while it runs.",
				run:  run,
			}
			if m.enrolledOwnerOK {
				row.name, row.value = "Re-enroll your voice", m.enrolledOwner.Name
				if m.ownerRevIsStale() {
					row.detail = "re-enroll to refresh"
				}
			}
		case speakerRowRemove:
			row = settingRow{
				name: "Remove your voice", kind: rowAction, verb: "remove", value: "nothing registered",
				help: "Deletes your stored voiceprint. Asks first.",
				run:  run,
			}
			if m.enrolledOwnerOK {
				row.value = m.enrolledOwner.Name
			}
		case speakerRowEnrollOther:
			row = settingRow{
				name: "Enroll another speaker…", kind: rowAction, verb: "name them",
				help: "Records a guest's voice under a name you type.",
				run:  run,
			}
		case speakerRowGuest:
			row = settingRow{
				name: r.name, kind: rowAction, verb: "re-enroll",
				help: "Records this speaker again.",
				run:  run,
			}
			if strings.Contains(r.label, "re-enroll to refresh") {
				row.detail = "re-enroll to refresh"
			}
		default:
			continue
		}
		rows = append(rows, row)
	}
	var guests []string
	for _, p := range m.enrolledProfiles {
		if !p.Owner {
			guests = append(guests, p.Name)
		}
	}
	if len(guests) > 0 {
		rows = append(rows, settingRow{
			name: "Remove an enrolled speaker…", kind: rowMenu, verb: "choose",
			options: guests, current: -1,
			help: "Deletes that speaker's voiceprint. Asks first.",
			set: func(m *settingsModel, i int) tea.Cmd {
				m.beginRemoveVoiceNamed(guests[i])
				return nil
			},
		})
	}
	return rows
}

func nearestIndex32(choices []float32, value float32) int {
	idx := -1
	best := float32(-1)
	for i, choice := range choices {
		delta := choice - value
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < best {
			best, idx = delta, i
		}
	}
	return idx
}

func deviceOptions(items []string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = deviceLabel(item)
	}
	return out
}

func (m settingsModel) inputRows() []settingRow {
	row := settingRow{
		key: "input_device", name: "Microphone", kind: rowPick,
		applies: "applies when returning to the conversation",
		help:    "Which microphone it listens to.",
	}
	if m.devicesLoading {
		row.disabled = true
		row.reason = "Discovering audio devices…"
		return []settingRow{row}
	}
	devices := m.inputItems
	row.value = deviceLabel(m.cfg.InputDevice)
	row.options = deviceOptions(devices)
	row.current = indexOf(devices, m.cfg.InputDevice)
	row.set = func(m *settingsModel, i int) tea.Cmd { return m.selectInputDevice(devices[i]) }
	return []settingRow{row}
}

func (m settingsModel) outputRows() []settingRow {
	speaker := settingRow{
		key: "output_device", name: "Speaker", kind: rowPick,
		applies: appliesNextConversation,
		help:    "Which loudspeaker it comes out of. Previews use it now.",
	}
	if m.devicesLoading {
		speaker.disabled = true
		speaker.reason = "Discovering audio devices…"
	} else {
		devices := m.outputItems
		speaker.value = deviceLabel(m.cfg.OutputDevice)
		speaker.options = deviceOptions(devices)
		speaker.current = indexOf(devices, m.cfg.OutputDevice)
		speaker.set = func(m *settingsModel, i int) tea.Cmd { return m.selectOutputDevice(devices[i]) }
	}
	volume := settingRow{
		key: "playback_volume", name: "Volume", kind: rowSlider,
		value: m.volumeValue(), applies: appliesNow,
		help: "Playback level for the conversation and for previews.",
		nudge: func(m *settingsModel, delta int) tea.Cmd {
			m.adjustVolume(float64(delta) * playbackVolumeStep)
			return nil
		},
	}
	return []settingRow{speaker, volume}
}

func (m settingsModel) meetingRows() []settingRow {
	cfg := m.cfg
	run := func(row int) func(*settingsModel) tea.Cmd {
		return func(m *settingsModel) tea.Cmd {
			m.selectMeetingRow(row)
			return nil
		}
	}
	diarizationOn := cfg.Speaker.Enabled && cfg.Speaker.Meeting.Enabled
	liveOn := cfg.Speaker.Enabled && cfg.Speaker.Meeting.Live
	engine, engineErr := config.MeetingDiarizerFrom(cfg)
	engines := config.MeetingDiarizers()
	engineLabels := make([]string, len(engines))
	for i, e := range engines {
		switch e {
		case config.DiarizerSortformer:
			engineLabels[i] = e + " — NVIDIA, through transcribe.cpp (optional)"
		default:
			engineLabels[i] = e + " — pyannote + TitaNet (default)"
		}
	}
	engineRow := settingRow{
		key: "speaker.meeting.diarizer", name: "Diarization engine", kind: rowPick,
		value: engine, options: engineLabels, current: indexOf(engines, engine),
		applies: appliesNextMeeting,
		help:    "Which engine labels speakers in the review pass. Live labels stay on sherpa.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.setMeetingDiarizer(engines[i]) },
	}
	if engineErr != nil {
		engineRow.value = strings.TrimSpace(cfg.Speaker.Meeting.Diarizer)
		engineRow.detail = "unknown value"
		engineRow.current = -1
	} else if engine == config.DiarizerSortformer {
		if gap := m.meetingDiarizerGap(); gap.Missing() {
			engineRow.detail = gap.Detail
		}
	}
	numLabels := make([]string, len(meetingNumSpeakerChoices))
	numCurrent := -1
	for i, n := range meetingNumSpeakerChoices {
		numLabels[i] = "Auto"
		if n > 0 {
			numLabels[i] = fmt.Sprintf("%d", n)
		}
		if n == cfg.Speaker.Meeting.NumSpeakers {
			numCurrent = i
		}
	}
	modes := []string{"ask", "auto", "off"}
	mode := cfg.Meeting.Route.Mode
	if mode == "" {
		mode = "ask"
	}
	bodies := []string{"notes", "full"}
	body := cfg.Meeting.Route.Body
	if body == "" {
		body = "notes"
	}
	dests := m.defaultDestinationChoices()
	destLabels := make([]string, len(dests))
	destCurrent := -1
	for i, d := range dests {
		destLabels[i] = d.id
		if d.id == "" {
			destLabels[i] = "(launch campaign)"
		}
		if d.id == cfg.Meeting.Route.Default {
			destCurrent = i
		}
	}
	destValue := cfg.Meeting.Route.Default
	if destValue == "" {
		destValue = "(launch campaign)"
	}
	availLabel := fmt.Sprintf("%d available", len(m.routeDests))
	if m.routeDestsLoading {
		availLabel = "discovering…"
	} else if m.routeDestsErr != "" {
		availLabel = "discovery error"
	}
	return []settingRow{
		{
			key: "speaker.meeting.enabled", name: "Speaker diarization", kind: rowToggle,
			value: onOff(diarizationOn), applies: appliesNextMeeting,
			help: "Label who said what in the review pass after a meeting.",
			run:  run(meetingRowDiarization),
		},
		{
			key: "speaker.meeting.live", name: "Live labels while recording", kind: rowToggle,
			value: onOff(liveOn), applies: appliesNextMeeting,
			help: "Provisional speaker-N labels while a meeting records.",
			run:  run(meetingRowLiveLabels),
		},
		engineRow,
		{
			key: "speaker.meeting.num_speakers", name: "Expected speakers", kind: rowChoice,
			value:   meetingNumSpeakersLabel(cfg.Speaker.Meeting.NumSpeakers, engine),
			options: numLabels, current: numCurrent, applies: appliesNextMeeting,
			help: "Fix the speaker count for 1:1 interviews and small groups; Auto can over-split.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setMeetingNumSpeakers(meetingNumSpeakerChoices[i]) },
		},
		{
			key: "speaker.meeting.record_audio", name: "Record audio for analysis", kind: rowToggle,
			value: onOff(cfg.Speaker.Meeting.RecordAudio), applies: appliesNextMeeting,
			help: "Keep the meeting audio so the review pass can run again.",
			run:  run(meetingRowRecordAudio),
		},
		{
			key: "meeting.route.mode", name: "Route mode", kind: rowChoice,
			value: mode, options: modes, current: indexOf(modes, mode), applies: appliesNextMeeting,
			help: "ask: choose where notes go at the end · auto: send to the default · off: keep them here.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setRouteMode(modes[i]) },
		},
		{
			key: "meeting.route.default", name: "Default destination", kind: rowPick,
			value: destValue, options: destLabels, current: destCurrent, applies: appliesNextMeeting,
			help: "Where notes go when route mode is auto.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setDefaultDestinationChoice(dests[i]) },
		},
		{
			key: "meeting.route.body", name: "Body scope", kind: rowChoice,
			value: body, options: bodies, current: indexOf(bodies, body), applies: appliesNextMeeting,
			help: "notes: the summary · full: the whole transcript.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setBodyScope(bodies[i]) },
		},
		{
			name: "Configured destinations", kind: rowInfo, verb: "where",
			value: fmt.Sprintf("%d", len(cfg.Meeting.Route.Destinations)),
			help:  "Destinations written in the config file.",
			run:   run(meetingRowConfigured),
		},
		{
			name: "Picker destinations", kind: rowInfo, verb: "list",
			value: availLabel,
			help:  "Destinations camp discovered, offered when a meeting ends.",
			run:   run(meetingRowPicker),
		},
		{
			name: "Refresh destinations", kind: rowAction, verb: "refresh",
			detail: "camp list + config",
			help:   "Re-run destination discovery.",
			run: func(m *settingsModel) tea.Cmd {
				m.selectMeetingRow(meetingRowRefresh)
				return m.loadRouteDestinations()
			},
		},
	}
}
