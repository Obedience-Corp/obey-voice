package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

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
