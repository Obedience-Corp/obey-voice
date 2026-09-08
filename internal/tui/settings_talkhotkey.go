package tui

import (
	"fmt"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// talkComboLabel is the combo as this OS spells it, marked when it is the
// platform default rather than a choice, or when the stored value does not
// parse and the conversation will refuse it.
func (m settingsModel) talkComboLabel() string {
	combo, err := config.TalkHotkeyComboOf(m.cfg)
	if err != nil {
		return strings.TrimSpace(m.cfg.TalkHotkey) + " (invalid)"
	}
	if strings.TrimSpace(m.cfg.TalkHotkey) == "" {
		return combo.Display() + " (default)"
	}
	return combo.Display()
}

// talkModeCopy is the one line each mode gets in the status message.
func talkModeCopy(mode talkhotkey.Mode) string {
	switch mode {
	case talkhotkey.ModeToggle:
		return "tap to start, tap to send"
	case talkhotkey.ModeAutomatic:
		return "a tap toggles, a longer press is a hold"
	default:
		return "press to talk, release to send"
	}
}

// cycleTalkMode steps hold → toggle → automatic → hold and persists it.
func (m *settingsModel) cycleTalkMode() tea.Cmd {
	modes := talkhotkey.Modes()
	current := string(config.TalkHotkeyModeOf(m.cfg))
	next := modes[0]
	for i, mode := range modes {
		if mode == current {
			next = modes[(i+1)%len(modes)]
			break
		}
	}
	if err := m.save("talk_hotkey_mode", next); err != nil {
		m.message = fmt.Sprintf("Failed to save talk key mode: %v", err)
		return nil
	}
	m.cfg.TalkHotkeyMode = next
	m.buildToolItems()
	m.message = fmt.Sprintf("Talk key mode %s: %s; applies to the next conversation", next, talkModeCopy(talkhotkey.Mode(next)))
	return nil
}

// beginTalkComboEdit opens the one-line editor on the stored spelling; the
// placeholder is the platform default, which an empty value means.
func (m *settingsModel) beginTalkComboEdit() tea.Cmd {
	return m.beginEdit(editTalkHotkey, "Talk key combo", strings.TrimSpace(m.cfg.TalkHotkey))
}

// commitTalkHotkey saves the typed combo in its canonical spelling, or the
// platform default when the field is empty. A combo the conversation would
// refuse keeps the editor open with the reason, so the value is fixed here
// rather than discovered on the next launch.
func (m settingsModel) commitTalkHotkey(value string) (settingsModel, tea.Cmd) {
	value = strings.TrimSpace(value)
	stored := ""
	if value != "" {
		combo, err := talkhotkey.ParseCombo(value)
		if err != nil {
			m.message = err.Error()
			return m, nil
		}
		stored = combo.String()
	}
	if err := m.save("talk_hotkey", stored); err != nil {
		m.message = fmt.Sprintf("Failed to save talk key combo: %v", err)
		return m, nil
	}
	m.cancelEdit()
	m.cfg.TalkHotkey = stored
	m.buildToolItems()
	combo, _ := config.TalkHotkeyComboOf(m.cfg)
	if stored == "" {
		m.message = fmt.Sprintf("Talk key back to the default %s; applies to the next conversation", combo.Display())
	} else {
		m.message = fmt.Sprintf("Talk key %s; applies to the next conversation", combo.Display())
	}
	if !talkhotkey.Supported() {
		m.message += " · on " + runtime.GOOS + " the key is whatever you bind to obey-voice talk; this only names it"
	}
	return m, nil
}
