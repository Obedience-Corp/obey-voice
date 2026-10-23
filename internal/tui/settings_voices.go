package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// settingsVoicesMsg carries the Voice list's catalog fetch back to Settings.
type settingsVoicesMsg voiceCatalogResult

// loadVoiceCatalog asks the engine for its voices when it has to be asked
// (openai-speech) and drops the live list when it does not.
func (m *settingsModel) loadVoiceCatalog() tea.Cmd {
	fetch := m.voices.fetch(m.cfg, activeTTSProvider(m.cfg))
	if fetch == nil {
		return nil
	}
	return func() tea.Msg { return settingsVoicesMsg(fetch()) }
}

// applyVoiceCatalog lands a fetch; a failure keeps the static list and the
// Voice heading says why.
func (m *settingsModel) applyVoiceCatalog(msg settingsVoicesMsg) {
	m.keepFocus(func() {
		if m.voices.apply(voiceCatalogResult(msg)) {
			m.buildVoiceItems()
		}
	})
}

// withVoice keeps the configured voice listed when a catalog did not name
// it, so the ✓ can still be seen and previewed.
func withVoice(voices []tts.Voice, name string) []tts.Voice {
	name = strings.TrimSpace(name)
	if name == "" {
		return voices
	}
	for _, v := range voices {
		if strings.EqualFold(v.Name, name) {
			return voices
		}
	}
	return append(append([]tts.Voice(nil), voices...), tts.Voice{Name: name, FriendlyName: name})
}

func (m settingsModel) activeVoice() string {
	return config.TTSVoiceFor(m.cfg, activeTTSProvider(m.cfg))
}

func (m settingsModel) voiceLocales() []string { return voiceLocalesOf(m.voiceItems) }

// activeVoiceLocale is the filter in force: "" for (all), or when the list
// no longer has the locale it names.
func (m settingsModel) activeVoiceLocale() string {
	if m.voiceLocale == "" {
		return ""
	}
	locales := m.voiceLocales()
	if idx := indexOf(locales, m.voiceLocale); idx > 0 {
		return locales[idx]
	}
	return ""
}

// shownVoiceItems is the voice list under the Language filter. The ✓ voice
// is never filtered away.
func (m settingsModel) shownVoiceItems() []tts.Voice {
	return voicesInLocale(m.voiceItems, m.activeVoiceLocale(), m.activeVoice())
}

// voiceFilterRows is the Language filter above the voice list, for a list
// that states more than one locale. It is view state: choosing writes
// nothing, and the list under it changes at once.
func (m settingsModel) voiceFilterRows() []settingRow {
	locales := m.voiceLocales()
	if len(locales) < 3 {
		return nil
	}
	current := max(indexOf(locales, m.activeVoiceLocale()), 0)
	return []settingRow{{
		name: "Language filter", kind: rowChoice,
		value: locales[current], options: locales, current: current,
		help: "Shows only the voices in one language; voices that state none stay listed. Nothing is saved.",
		set: func(m *settingsModel, i int) tea.Cmd {
			m.voiceLocale = ""
			if i > 0 && i < len(locales) {
				m.voiceLocale = locales[i]
			}
			return nil
		},
	}}
}

func voiceIndex(voices []tts.Voice, name string) int {
	for i, v := range voices {
		if strings.EqualFold(v.Name, name) {
			return i
		}
	}
	return -1
}

// focusedVoice is the voice under the cursor, when it is on the voice list.
func (m settingsModel) focusedVoice() (string, bool) {
	row, line, ok := m.focused()
	if !ok || line.option < 0 || row.name != "Voice" {
		return "", false
	}
	shown := m.shownVoiceItems()
	if line.option >= len(shown) {
		return "", false
	}
	return shown[line.option].Name, true
}

// voiceLine is the section line of option i of the voice list, or -1.
func (m settingsModel) voiceLine(i int) int {
	if i < 0 {
		return -1
	}
	rows, lines := m.layout()
	for n, l := range lines {
		if l.focusable() && rows[l.row].name == "Voice" && l.option == i {
			return n
		}
	}
	return -1
}
