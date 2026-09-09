package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type settingsSection int

const (
	sectionProvider settingsSection = iota
	sectionModel
	// The worker sits beside the brain because it is chosen with the brain:
	// picking what talks without picking what works is how a spawn ends up on
	// a model nobody selected.
	sectionWorker
	sectionTools
	sectionTTS
	sectionQwen
	sectionVoice
	sectionLanguage
	sectionSTT
	sectionSpeakers
	sectionInput
	sectionOutput
	sectionMeeting
	settingsSectionCount
)

type settingsModel struct {
	cfg       *config.Config
	providers []discovery.ProviderInfo

	section settingsSection
	cursor  int
	width   int
	height  int
	offset  int
	// cursors remembers the row each section was on, so leaving a section
	// and coming back does not throw the place away.
	cursors [settingsSectionCount]int
	// picker is the option list behind a pick or menu row. While it is up
	// it owns every key.
	picker settingsPicker

	// Derived lists for current section.
	providerItems  []string
	modelItems     []string
	workerItems    []workerItem
	toolItems      []string
	ttsItems       []ttsSettingItem
	qwenItems      []qwenOptionItem
	sttItems       []sttOptionItem
	voiceItems     []tts.Voice
	languageItems  []string
	inputItems     []string
	outputItems    []string
	devicesLoading bool
	deviceChecker  config.VoiceDeviceChecker

	// Meeting route discovery (camp list + config).
	routeDests        []meeting.Destination
	routeDestsLoading bool
	routeDestsErr     string
	routeDestsSeq     int

	// Preview playback. ensureTTSAssets is also the Qwen install seam.
	preview         voicePreview
	ensureTTSAssets func(ctx context.Context, cfg *config.Config, onProgress func(name string, pct float64)) error
	newTTSProvider  func(*config.Config) (tts.Provider, func(), error)
	saveConfig      func(string, any) error
	// onPlaybackVolume applies gain to the live conversation player. Nil when
	// Settings is open without a running pipeline (launcher).
	onPlaybackVolume func(float64)
	// lookPath resolves external binaries for the recognizer rows; nil uses
	// exec.LookPath. Tests inject a fake so a picker never depends on what
	// happens to be installed on the machine running them.
	lookPath func(string) (string, error)
	message  string

	// edit is the inline value editor (speech computer address / token /
	// speaker name). While it is active it owns every key.
	edit settingsEditor

	// Voice registration (Speakers → Register your voice). captureFor yields
	// the conversation's own capture graph when one is running — enrollment
	// records on the graph live analysis verifies on, never a second mic
	// pipeline. A nil captureFor means no microphone is reachable from here.
	captureFor      func() (speaker.CaptureSource, func(), error)
	enrollmentStore func() (*speaker.Enrollment, error)
	speakerEngine   func(ctx context.Context) (speaker.Engine, func(), error)
	liveSeeder      func() LiveSpeakerController
	// enrollGap is the pause between clips; tests set it to zero.
	enrollGap time.Duration
	enroll    enrollSession
	// enrollAfterName resumes a registration that stopped to ask for a name.
	enrollAfterName bool
	// confirmRemoveVoice holds the armed removal's subject ("" = not armed).
	confirmRemoveVoice string
	// enrolledProfiles is store.List() cached so rendering never hits disk.
	// enrolledOwner is the Owner() profile when enrolledOwnerOK is true.
	enrolledProfiles []speaker.Profile
	enrolledOwner    speaker.Profile
	enrolledOwnerOK  bool

	qwenStatus        managedqwen.Status
	nativeStatus      managedqwen.NativeStatus
	qwenInstalling    bool
	qwenTierPending   string
	qwenInstallCancel context.CancelFunc
	qwenInstallEvents *eventBridge
	ensureQwen        func(context.Context, string, managedqwen.ProgressFunc) (managedqwen.Status, error)
}

func newSettings(cfg *config.Config, providers []discovery.ProviderInfo) settingsModel {
	ensure := func(ctx context.Context, cfg *config.Config, onProgress func(name string, pct float64)) error {
		return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, onProgress)
	}
	m := settingsModel{
		cfg:           cfg,
		providers:     providers,
		deviceChecker: audio.NewDeviceChecker(),
		preview: voicePreview{
			newPreviewPlayer: func() audio.Engine {
				p := audio.NewPlayerWithDevice(cfg.OutputDevice)
				p.SetVolume(config.PlaybackVolumeOf(cfg))
				return p
			},
			ensure: ensure,
			newTTS: tts.NewProvider,
		},
		ensureTTSAssets: ensure,
		newTTSProvider:  tts.NewProvider,
		saveConfig:      config.SetAndSave,
		ensureQwen:      managedqwen.Ensure,
	}
	m.initEnrollSeams(cfg)
	m.refreshQwenStatus()
	m.buildProviderItems()
	m.buildModelItems()
	m.buildWorkerItems()
	m.buildToolItems()
	m.buildTTSItems()
	m.buildQwenItems()
	m.buildVoiceItems()
	m.buildLanguageItems()
	m.buildSTTItems()
	m.inputItems = []string{""}
	m.outputItems = []string{""}
	return m
}

type deviceListsMsg struct {
	inputs  []string
	outputs []string
	err     error
}

func (m *settingsModel) loadDevices() tea.Cmd {
	m.devicesLoading = true
	checker := m.deviceChecker
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		inputs, err := checker.CaptureDevices(ctx)
		if err != nil {
			return deviceListsMsg{err: err}
		}
		outputs, err := checker.PlaybackDevices(ctx)
		return deviceListsMsg{inputs: inputs, outputs: outputs, err: err}
	}
}

func (m *settingsModel) buildProviderItems() {
	m.providerItems = nil
	for _, p := range m.providers {
		label := p.Name
		if !p.Available {
			label += " (not available)"
		}
		m.providerItems = append(m.providerItems, label)
	}
}

func (m *settingsModel) buildModelItems() {
	m.modelItems = nil
	for _, p := range m.providers {
		if p.Name == m.cfg.BrainProvider {
			m.modelItems = p.Models
			break
		}
	}
	if len(m.modelItems) == 0 {
		m.modelItems = []string{"default"}
	}
}

// Tools tab row indices — keep in lockstep with buildToolItems().
// Combined from #212 (barge-in/AEC) and #213 (mouse scroll).
const (
	toolRowLocalTools    = 0
	toolRowSkills        = 1
	toolRowBargeIn       = 2
	toolRowNameSummon    = 3
	toolRowVoiceFrontend = 4
	toolRowMouse         = 5
	toolRowTalkKey       = 6
	toolRowTalkMode      = 7
	toolRowTalkCombo     = 8
	toolRowCount         = 9
)

func (m *settingsModel) buildToolItems() {
	m.toolItems = []string{
		fmt.Sprintf("Local tools — %s", enabledLabel(m.cfg.VoiceToolsEnabled)),
	}
	// Agent Skills discovery via SKILL.md is Ollama-only; Claude/Grok use CLIs.
	if strings.EqualFold(m.cfg.BrainProvider, "ollama") {
		m.toolItems = append(m.toolItems,
			fmt.Sprintf("Agent Skills (Ollama) — %s", enabledLabel(m.cfg.SkillsEnabled)),
		)
	} else {
		m.toolItems = append(m.toolItems, "Agent Skills — n/a (Ollama only)")
	}
	// Voice barge-in is opt-in (default off): without a solid AEC path the mic
	// hears TTS playback and self-interrupts. Voice frontend is the AEC/NS/AGC
	// chain doctor recommends when barge-in is on.
	// Name summon needs the same front end for a sharper reason: without it the
	// agent hears its own name in playback (D5). Aliases stay a config-file key.
	m.toolItems = append(m.toolItems,
		fmt.Sprintf("Voice barge-in — %s", enabledLabel(m.cfg.BargeInEnabled)),
		fmt.Sprintf("Name summon — %s", enabledLabel(m.cfg.NameSummonEnabled)),
		fmt.Sprintf("Voice frontend (AEC) — %s", enabledLabel(m.cfg.VoiceFrontendEnabled)),
	)
	// Off by default: the terminal keeps unmodified drag-select, and the wheel
	// still scrolls chat through arrow-key translation while the composer is
	// empty. ON routes the wheel straight to the viewport — smoother while
	// drafting — at the cost of needing option/fn/shift to select. Page
	// Up/Down scroll either way.
	m.toolItems = append(m.toolItems,
		fmt.Sprintf("Mouse scroll (claims mouse) — %s", enabledLabel(m.cfg.TUIMouseEnabled)),
	)
	// The system-wide talk key. The conversation's own conflict message sends
	// people here to rebind it, so here is where it is.
	m.toolItems = append(m.toolItems,
		fmt.Sprintf("Talk key — %s", enabledLabel(m.cfg.TalkHotkeyEnabled)),
		fmt.Sprintf("Talk key mode — %s", config.TalkHotkeyModeOf(m.cfg)),
		fmt.Sprintf("Talk key combo — %s", m.talkComboLabel()),
	)
}

// nameSummonWouldSelfTrigger answers the question the Tools row asks before it
// saves: would turning name summon on leave the agent able to hear its own name
// in playback (D5)? Same predicate the doctor and the conversation use, applied
// to a copy that already has the toggle flipped.
//
// The extra clause covers the AEC row beneath it, which writes the legacy
// voice_frontend_enabled bool — that only becomes a front end on the next load,
// and ignoring it would refuse name summon to someone who just turned the front
// end on in this very screen.
func (m *settingsModel) nameSummonWouldSelfTrigger() bool {
	next := *m.cfg
	next.NameSummonEnabled = true
	if next.VoiceFrontendEnabled && config.VoiceFrontendOf(&next) == config.VoiceFrontendOff {
		next.VoiceFrontend = config.VoiceFrontendNLMS
	}
	return config.NameSummonSelfTriggerRisk(&next)
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "ON ✓"
	}
	return "OFF"
}

// selectToolRow toggles a Tools row (by toolRow* index) and persists it.
// Pipeline-level knobs (barge-in, voice frontend) only take effect after the
// conversation is rebuilt; mouse claim applies on next enter/leave of chat.
func (m *settingsModel) selectToolRow(row int) tea.Cmd {
	if row < 0 || row >= len(m.toolItems) {
		return nil
	}
	switch row {
	case toolRowTalkMode:
		return m.cycleTalkMode()
	case toolRowTalkCombo:
		return m.beginTalkComboEdit()
	}
	var (
		key   string
		value bool
		label string
		field *bool
	)
	switch row {
	case toolRowTalkKey:
		key, label = "talk_hotkey_enabled", "Talk key"
		value = !m.cfg.TalkHotkeyEnabled
		field = &m.cfg.TalkHotkeyEnabled
	case toolRowLocalTools:
		key, label = "voice_tools_enabled", "Local tools"
		value = !m.cfg.VoiceToolsEnabled
		field = &m.cfg.VoiceToolsEnabled
	case toolRowSkills:
		if !strings.EqualFold(m.cfg.BrainProvider, "ollama") {
			m.message = "Agent Skills apply only when brain provider is Ollama"
			return nil
		}
		key, label = "skills_enabled", "Agent Skills"
		value = !m.cfg.SkillsEnabled
		field = &m.cfg.SkillsEnabled
	case toolRowBargeIn:
		key, label = "barge_in_enabled", "Voice barge-in"
		value = !m.cfg.BargeInEnabled
		field = &m.cfg.BargeInEnabled
	case toolRowNameSummon:
		if !m.cfg.NameSummonEnabled && m.nameSummonWouldSelfTrigger() {
			m.message = "Name summon needs the voice front end on — enable Voice frontend (AEC) first — so playback of the name cannot summon the agent."
			return nil
		}
		key, label = "name_summon_enabled", "Name summon"
		value = !m.cfg.NameSummonEnabled
		field = &m.cfg.NameSummonEnabled
	case toolRowVoiceFrontend:
		key, label = "voice_frontend_enabled", "Voice frontend (AEC)"
		value = !m.cfg.VoiceFrontendEnabled
		field = &m.cfg.VoiceFrontendEnabled
	case toolRowMouse:
		key, label = "tui_mouse_enabled", "Mouse scroll"
		value = !m.cfg.TUIMouseEnabled
		field = &m.cfg.TUIMouseEnabled
	default:
		return nil
	}
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	if err := saveConfig(key, value); err != nil {
		m.message = fmt.Sprintf("Failed to save %s: %v", label, err)
		return nil
	}
	*field = value
	m.buildToolItems()
	switch row {
	case toolRowTalkKey:
		m.message = fmt.Sprintf("Talk key %s; applies to the next conversation", enabledLabel(value))
	case toolRowMouse:
		// Mouse claim is re-evaluated on conversation enter/leave — no full restart.
		if value {
			m.message = "Mouse scroll ON · wheel scrolls chat; hold option/fn/shift to select text · applies when you return to chat"
		} else {
			m.message = "Mouse scroll OFF · unmodified drag-select works · wheel scrolls chat when the composer is empty, PgUp/PgDn always · applies when you return to chat"
		}
	default:
		// Pipeline knobs (and tools/skills) are not hot-reloaded when /settings
		// returns to a live conversation — only a new conversation or process
		// restart rebuilds capture/VAD/frontend/BargeInVAD.
		m.message = fmt.Sprintf("%s %s; restart app or start a new conversation to apply", label, enabledLabel(value))
		if row == toolRowBargeIn && value && !m.cfg.VoiceFrontendEnabled {
			m.message += " · enable Voice frontend (AEC) to reduce echo false-triggers"
		}
		if row == toolRowVoiceFrontend && value && !m.cfg.BargeInEnabled {
			m.message += " · can over-suppress normal speech; intended with barge-in"
		}
		if row == toolRowVoiceFrontend && !value && m.cfg.BargeInEnabled {
			m.message += " · barge-in is on; echo may self-interrupt without AEC"
		}
	}
	return nil
}

type ttsSettingItem struct {
	provider string
	detail   string
}

func (m *settingsModel) buildTTSItems() {
	m.ttsItems = nil
	for _, spec := range tts.Providers() {
		detail := ttsProviderDetail(spec, m.cfg)
		if spec.Name == managedqwen.ProviderName {
			managed := managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel)
			switch {
			case m.qwenInstalling:
				detail = "installing Qwen assets…"
			case managed && m.nativeStatus.Installed:
				detail = fmt.Sprintf("native worker · tier %s · presets ready", m.nativeStatus.DefaultTier)
			case managed:
				detail = "native package not installed · open Qwen tab or enter to install"
			}
		}
		m.ttsItems = append(m.ttsItems, ttsSettingItem{
			provider: spec.Name,
			detail:   detail,
		})
	}
}

func (m *settingsModel) buildVoiceItems() {
	m.voiceItems = nil
	if qwenUsesPresets(m.cfg, m.nativeStatus, m.qwenStatus) {
		m.voiceItems = append(m.voiceItems, qwenPresetVoices(m.cfg, m.nativeStatus)...)
		return
	}
	// A managed qwen3-tts selection that isn't installed falls through to
	// here (qwenUsesPresets is false until native.Installed): tts.StaticVoices
	// now publishes the CustomVoice preset catalog unconditionally (it exists
	// whether or not the package is on disk, per MDL-A5), but this section
	// must still show its "not installed" guidance instead of a voice list
	// nothing can speak yet — that guidance is what qwenUsesPresets==false
	// otherwise signals.
	if strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) {
		return
	}
	// StaticVoicesFor, not StaticVoices: audio8's catalog is the packaged
	// reference plus whatever this config's models_dir has registered.
	voices, err := tts.StaticVoicesFor(m.cfg, m.cfg.TTSProvider, "", "")
	if err != nil {
		return
	}
	m.voiceItems = append(m.voiceItems, voices...)
}

func (m *settingsModel) buildLanguageItems() {
	m.languageItems = nil
	// Language list is available whenever managed/native Qwen selection is active
	// (empty binary/model), even before install completes.
	if m.cfg != nil &&
		strings.EqualFold(activeTTSProvider(m.cfg), managedqwen.ProviderName) &&
		managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel) {
		m.languageItems = managedqwen.SupportedLanguages()
	}
}

func (m settingsModel) activeModel() string {
	switch m.cfg.BrainProvider {
	case "ollama":
		if m.cfg.OllamaModel != "" {
			return m.cfg.OllamaModel
		}
	case "grok":
		if m.cfg.GrokModel != "" {
			return m.cfg.GrokModel
		}
	case "cursor":
		if m.cfg.CursorModel != "" {
			return m.cfg.CursorModel
		}
	}
	return "default"
}

func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureCursorVisible()

	case tea.KeyMsg:
		if m.edit.kind != editNone {
			return m.updateEditor(msg)
		}
		// A registration, an armed removal, or an open picker owns every
		// key while it is up.
		if m.enroll.active {
			return m.updateEnrollKey(msg)
		}
		if m.confirmRemoveVoice != "" {
			return m.updateRemoveVoiceConfirm(msg)
		}
		if m.picker.active {
			return m.updatePicker(msg)
		}
		// One key, one verb: ←/→ change the focused value and never switch
		// section; tab and [ ] move between sections; enter opens; space
		// toggles. Bare letters other than the vim motions and p do nothing.
		var cmd tea.Cmd
		switch msg.String() {
		case "tab", "]":
			cmd = m.switchSettingsSection(1)
		case "shift+tab", "[":
			cmd = m.switchSettingsSection(-1)
		case "right", "l":
			cmd = m.adjustRow(1)
		case "left", "h":
			cmd = m.adjustRow(-1)
		case "up", "k":
			// Moving on is reading the message; the next row's detail line
			// must not stay hidden behind it.
			m.message = ""
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			m.message = ""
			if max := m.currentListLen() - 1; m.cursor < max {
				m.cursor++
			}
		case "enter":
			cmd = m.activateRow()
		case " ":
			cmd = m.toggleRow()
		case "p":
			cmd = m.previewRow()
		case "esc", "q":
			m.cancelPreview()
			return m, func() tea.Msg { return settingsDoneMsg{} }
		}
		m.clampCursor()
		m.ensureCursorVisible()
		return m, cmd

	case enrollProgressMsg, enrollEnrollingMsg, enrollClosedMsg, enrollDoneMsg:
		return m.updateEnroll(msg)

	case voicePreviewDoneMsg:
		if m.preview.applyDone(msg) && msg.message != "" {
			m.message = msg.message
		}

	case qwenInstallDoneMsg:
		m.qwenInstalling = false
		m.qwenInstallCancel = nil
		if tier := m.qwenTierPending; tier != "" {
			// Tier-row upgrade: save the tier once its model is really on
			// disk; never flip the TTS provider as a side effect.
			m.qwenTierPending = ""
			if msg.err != nil {
				m.message = fmt.Sprintf("Tier %s install failed: %v", tier, msg.err)
				m.buildQwenItems()
				break
			}
			if !msg.native.ModelReady {
				m.message = fmt.Sprintf("Package installed but tier %s is still missing — the download source has no %s model", tier, tier)
				m.refreshQwenStatus()
				m.buildQwenItems()
				break
			}
			save := m.saveConfig
			if save == nil {
				save = config.SetAndSave
			}
			if err := save("qwen_tts_model_tier", tier); err != nil {
				m.message = fmt.Sprintf("Tier %s installed but could not be saved: %v", tier, err)
				m.buildQwenItems()
				break
			}
			m.cfg.QwenTTSModelTier = tier
			m.nativeStatus = msg.native
			m.buildQwenItems()
			m.message = fmt.Sprintf("Qwen model tier set to %s — restart voice / new conversation to apply", tier)
			break
		}
		if msg.err != nil {
			m.message = fmt.Sprintf("Qwen setup failed: %v", msg.err)
			m.buildTTSItems()
			m.buildQwenItems()
			break
		}
		m.qwenStatus = msg.status
		if msg.native.Root != "" {
			m.nativeStatus = msg.native
		} else {
			m.refreshQwenStatus()
		}
		if err := m.activateQwenAfterInstall(); err != nil {
			m.message = fmt.Sprintf("Qwen installed but configuration could not be saved: %v", err)
			m.buildTTSItems()
			m.buildQwenItems()
			break
		}
		m.buildTTSItems()
		m.buildQwenItems()
		m.buildVoiceItems()
		m.buildLanguageItems()
		if m.nativeStatus.Installed {
			m.message = "Native Qwen package ready; open Voice for presets, Qwen tab for tier/consent/cache"
		} else if m.cfg != nil && managedqwen.ResolveNativeURL(m.cfg.QwenTTSNativeURL) == "" {
			m.message = "Qwen activated, but " + managedqwen.UnavailableHint()
		} else {
			m.message = "Qwen activated; open Settings → Qwen → Install package (or run models ensure --tts)"
		}

	case qwenInstallProgressMsg:
		if !m.qwenInstalling {
			break
		}
		if msg.pct > 0 {
			m.message = fmt.Sprintf("Qwen setup: %s (%d%%)", msg.stage, int(msg.pct))
		} else {
			m.message = fmt.Sprintf("Qwen setup: %s…", msg.stage)
		}
		if m.qwenInstallEvents != nil {
			return m, m.qwenInstallEvents.wait()
		}

	case qwenInstallProgressClosedMsg:
		m.qwenInstallEvents = nil

	case deviceListsMsg:
		m.devicesLoading = false
		if msg.err != nil {
			m.message = fmt.Sprintf("Audio device probe failed: %v", msg.err)
			break
		}
		m.inputItems = append([]string{""}, msg.inputs...)
		m.outputItems = append([]string{""}, msg.outputs...)

	case meetingRouteDestsMsg:
		if msg.seq != m.routeDestsSeq {
			break
		}
		m.routeDestsLoading = false
		// Soft-fail: still show configured (+ any) dests when camp list errors.
		m.routeDests = msg.dests
		if msg.err != nil {
			m.routeDestsErr = msg.err.Error()
			m.message = fmt.Sprintf("Found %d destination(s); camp list error: %v", len(msg.dests), msg.err)
			break
		}
		m.routeDestsErr = ""
		m.message = fmt.Sprintf("Found %d route destination(s)", len(msg.dests))
	}

	return m, nil
}

func (m *settingsModel) visibleRange(total int) (int, int) {
	visible := m.visibleRows()
	start := min(max(m.offset, 0), max(total-visible, 0))
	return start, min(start+visible, total)
}

func (m *settingsModel) ensureCursorVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	m.offset = max(m.offset, 0)
}

func (m settingsModel) visibleRows() int {
	h := m.height
	if h <= 0 {
		// No WindowSize yet — assume a normal terminal rather than a tiny list.
		h = 24
	}
	// Compact: title, tabs, footer. Full: title, tabs, rule, status, footer.
	// The list receives every remaining row so the body tracks terminal height.
	chrome := 5
	if h < 12 {
		chrome = 3
	}
	return max(h-chrome, 1)
}

func (m *settingsModel) currentListLen() int {
	return len(m.sectionRows())
}

// selectCurrent is enter on the focused row: open its list, flip it, edit
// it, or run it — whatever the row said it would do.
func (m *settingsModel) selectCurrent() tea.Cmd {
	return m.activateRow()
}

// selectProviderOption saves the brain provider at idx of the provider list
// and, when no worker has been chosen yet, lands on the Worker section: that
// is the picker's and enter's behaviour. stepProviderOption is the same save
// without the jump, for ←/→ stepping through the list on the row.
func (m *settingsModel) selectProviderOption(idx int) tea.Cmd {
	return m.setProviderOption(idx, true)
}

func (m *settingsModel) stepProviderOption(idx int) tea.Cmd {
	return m.setProviderOption(idx, false)
}

func (m *settingsModel) setProviderOption(idx int, landOnWorker bool) tea.Cmd {
	if idx < 0 || idx >= len(m.providers) {
		return nil
	}
	if !m.providers[idx].Available {
		m.message = fmt.Sprintf("%s is not available on this machine", m.providers[idx].Name)
		return nil
	}
	// Mutate the live config only after the save succeeds, so a failed save
	// doesn't leave the running session on a provider that was never
	// persisted.
	name := m.providers[idx].Name
	if err := config.SetAndSaveBrainProvider(m.cfg, name); err != nil {
		m.message = fmt.Sprintf("Failed to save provider: %v", err)
		return nil
	}
	m.buildModelItems()
	m.buildToolItems()
	m.buildWorkerItems()
	if strings.EqualFold(name, "ollama") && m.cfg.VoiceToolsEnabled {
		m.message = fmt.Sprintf("Provider set to %s · local tools on (Settings → Tools to toggle)", name)
	} else {
		m.message = fmt.Sprintf("Provider set to %s", name)
	}
	// Choosing a brain is choosing a worker: land on the Worker section
	// rather than leaving it to a default nobody picked — but only when the
	// choice was made, not on every step of ←/→ through the list.
	if landOnWorker && !config.WorkerSelected(m.cfg) {
		cmd := m.gotoSection(sectionWorker)
		m.message = fmt.Sprintf("Provider set to %s · now choose the worker that runs background jobs", name)
		return cmd
	}
	return nil
}

// selectModel saves a brain model for the current provider.
func (m *settingsModel) selectModel(model string) tea.Cmd {
	var field *string
	var key string
	switch m.cfg.BrainProvider {
	case "ollama":
		field, key = &m.cfg.OllamaModel, "ollama_model"
	case "grok":
		field, key = &m.cfg.GrokModel, "grok_model"
	case "cursor":
		field, key = &m.cfg.CursorModel, "cursor_model"
	}
	if field != nil {
		if err := config.SetAndSave(key, model); err != nil {
			m.message = fmt.Sprintf("Failed to save model: %v", err)
			return nil
		}
		*field = model
	}
	m.message = fmt.Sprintf("Model set to %s", model)
	return nil
}

// ttsProviderInstalls reports whether choosing provider would start the
// managed Qwen package install rather than just saving a key.
func (m settingsModel) ttsProviderInstalls(provider string) bool {
	return provider == managedqwen.ProviderName && !m.nativeStatus.Installed &&
		managedqwen.UseManaged(m.cfg.QwenTTSBinary, m.cfg.QwenTTSModel)
}

// selectTTSProvider saves the TTS provider, installing the managed Qwen
// package first when it is chosen and not yet on disk.
func (m *settingsModel) selectTTSProvider(provider string) tea.Cmd {
	if m.ttsProviderInstalls(provider) {
		if m.qwenInstalling {
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.qwenInstallCancel = cancel
		m.qwenInstalling = true
		m.qwenInstallEvents = newEventBridge(16)
		m.message = "Installing native Qwen3-TTS package (large download)…"
		m.buildTTSItems()
		m.buildQwenItems()
		return tea.Batch(m.qwenInstallEvents.wait(), m.installQwenAssets(ctx))
	}
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	if provider == managedqwen.ProviderName && m.nativeStatus.Installed {
		if err := m.saveManagedQwenDefaults(); err != nil {
			m.message = fmt.Sprintf("Failed to save Qwen voice defaults: %v", err)
			return nil
		}
	}
	// Settings writes global defaults only (WI-c8884d §5.2): a persona with
	// its own tts stack keeps it — edit the persona under Personas to change
	// a specific agent's voice.
	if err := saveConfig("tts_provider", provider); err != nil {
		m.message = fmt.Sprintf("Failed to save TTS provider: %v", err)
		return nil
	}
	m.cfg.TTSProvider = provider
	m.buildTTSItems()
	m.buildQwenItems()
	m.buildVoiceItems()
	m.buildLanguageItems()
	m.message = fmt.Sprintf("Default TTS provider set to %s; applies immediately unless the persona has its own voice (edit under Personas)", provider)
	return nil
}

// selectVoice saves a voice as the global default; persona voices are edited
// under Personas.
func (m *settingsModel) selectVoice(voice tts.Voice) tea.Cmd {
	key := config.TTSVoiceKeyFor(activeTTSProvider(m.cfg))
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	if err := saveConfig(key, voice.Name); err != nil {
		m.message = fmt.Sprintf("Failed to save voice: %v", err)
		return nil
	}
	config.SetTTSVoiceFor(m.cfg, activeTTSProvider(m.cfg), voice.Name)
	m.message = fmt.Sprintf("Default voice set to %s · personas with their own voice keep it", voice.Name)
	return nil
}

func (m *settingsModel) selectLanguage(language string) tea.Cmd {
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	if err := saveConfig("qwen_tts_language", language); err != nil {
		m.message = fmt.Sprintf("Failed to save language: %v", err)
		return nil
	}
	m.cfg.QwenTTSLanguage = language
	m.message = fmt.Sprintf("Qwen language set to %s", language)
	return nil
}

func (m *settingsModel) selectInputDevice(name string) tea.Cmd {
	if m.devicesLoading {
		return nil
	}
	if err := m.save("input_device", name); err != nil {
		m.message = fmt.Sprintf("Failed to save input device: %v", err)
		return nil
	}
	m.cfg.InputDevice = name
	m.message = "Microphone set to " + deviceLabel(name)
	return nil
}

func (m *settingsModel) selectOutputDevice(name string) tea.Cmd {
	if m.devicesLoading {
		return nil
	}
	if err := m.save("output_device", name); err != nil {
		m.message = fmt.Sprintf("Failed to save output device: %v", err)
		return nil
	}
	m.closePreview()
	m.cfg.OutputDevice = name
	m.message = "Speaker set to " + deviceLabel(name)
	return nil
}

type qwenInstallDoneMsg struct {
	status managedqwen.Status
	native managedqwen.NativeStatus
	err    error
}

type qwenInstallProgressMsg struct {
	stage string
	pct   float64
}

type qwenInstallProgressClosedMsg struct{}

func (m settingsModel) installManagedQwen(ctx context.Context) tea.Cmd {
	// Legacy entrypoint — prefer installQwenAssets (native-first ensure).
	return m.installQwenAssets(ctx)
}

func (m *settingsModel) activateQwenAfterInstall() error {
	if err := m.saveManagedQwenDefaults(); err != nil {
		return err
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("tts_provider", managedqwen.ProviderName); err != nil {
		return err
	}
	m.cfg.TTSProvider = managedqwen.ProviderName
	tier := strings.TrimSpace(m.cfg.QwenTTSModelTier)
	if tier == "" {
		tier = managedqwen.DefaultModelTier
		if err := save("qwen_tts_model_tier", tier); err != nil {
			return err
		}
		m.cfg.QwenTTSModelTier = tier
	}
	return nil
}

func (m *settingsModel) saveManagedQwenDefaults() error {
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	mode := strings.TrimSpace(m.cfg.QwenTTSMode)
	if mode == "" || mode == string(tts.VoiceModeStatic) {
		mode = string(tts.VoiceModeCustomVoice)
	}
	voice := strings.TrimSpace(m.cfg.QwenTTSVoice)
	if voice == "" || strings.EqualFold(voice, "default") {
		voice = managedqwen.DefaultVoice
	}
	language := strings.TrimSpace(m.cfg.QwenTTSLanguage)
	if language == "" {
		language = managedqwen.DefaultLanguage
	}
	values := []struct {
		key   string
		value string
	}{
		{"qwen_tts_mode", mode},
		{"qwen_tts_voice", voice},
		{"qwen_tts_language", language},
	}
	for _, item := range values {
		if err := save(item.key, item.value); err != nil {
			return err
		}
	}
	m.cfg.QwenTTSMode = mode
	m.cfg.QwenTTSVoice = voice
	m.cfg.QwenTTSLanguage = language
	return nil
}

func deviceLabel(name string) string {
	if name == "" {
		return "System default"
	}
	return name
}
