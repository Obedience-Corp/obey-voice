package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// personaVoicesMsg carries a Voice row catalog fetch back to the form.
type personaVoicesMsg voiceCatalogResult

// loadVoiceCatalog starts a fetch for the effective TTS provider when it has a
// live catalog, and clears any live state otherwise. The language filter is
// reset either way: locales belong to a catalog.
func (m *personasModel) loadVoiceCatalog() tea.Cmd {
	m.voiceLocale = ""
	fetch := m.voices.fetch(m.cfg, m.effectiveStackTTSProvider())
	if fetch == nil {
		return nil
	}
	return func() tea.Msg { return personaVoicesMsg(fetch()) }
}

func (m *personasModel) cancelVoiceCatalog() { m.voices.stop() }

// resetVoiceCatalog drops live state and retires any fetch in flight: its
// message, cancelled or not, is ignored when it lands.
func (m *personasModel) resetVoiceCatalog() { m.voices.reset() }

// applyVoiceCatalog lands a fetch. A failed fetch leaves the static list and
// says why on the row; the persona can still be saved with a typed voice.
func (m *personasModel) applyVoiceCatalog(msg personaVoicesMsg) {
	if m.voices.apply(voiceCatalogResult(msg)) && msg.err == nil {
		m.clampSelectedVoice()
	}
}

// voiceEntries is the catalog behind the Voice row for the effective
// provider: the live list when one landed, else the static one.
func (m personasModel) voiceEntries() []tts.Voice {
	provider := m.effectiveStackTTSProvider()
	if live := m.voices.landed(provider); live != nil {
		return live
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "qwen3-tts") {
		if voices, err := tts.StaticVoicesFor(m.cfg, provider, "", ""); err == nil {
			return voices
		}
	}
	names := stackVoiceCatalog(m.cfg, provider)
	out := make([]tts.Voice, 0, len(names))
	for _, name := range names {
		out = append(out, tts.Voice{Name: name, FriendlyName: name})
	}
	return out
}

// voiceLocales lists the Language row: (all) plus every locale the catalog
// states.
func (m personasModel) voiceLocales() []string { return voiceLocalesOf(m.voiceEntries()) }

// voiceLanguageUsable reports whether the Language row filters anything.
func (m personasModel) voiceLanguageUsable() bool { return len(m.voiceLocales()) > 2 }

func (m personasModel) voiceLocaleIndex() int {
	if m.voiceLocale == "" {
		return 0
	}
	for i, loc := range m.voiceLocales() {
		if i > 0 && strings.EqualFold(loc, m.voiceLocale) {
			return i
		}
	}
	return 0
}

// cycleVoiceLocale moves ←/→ through the languages; no-op for one language.
func (m *personasModel) cycleVoiceLocale(delta int) {
	if !m.voiceLanguageUsable() {
		return
	}
	list := m.voiceLocales()
	idx := (m.voiceLocaleIndex() + delta + len(list)) % len(list)
	if idx == 0 {
		m.voiceLocale = ""
		return
	}
	m.voiceLocale = list[idx]
}

// filteredVoiceEntries applies the Language row to the catalog.
func (m personasModel) filteredVoiceEntries() []tts.Voice {
	return voicesInLocale(m.voiceEntries(), m.voiceLocale, "")
}

// selectedVoiceEntry is the catalog entry for the selected voice, if listed.
func (m personasModel) selectedVoiceEntry() (tts.Voice, bool) {
	v := strings.TrimSpace(m.selectedVoice)
	if v == "" {
		return tts.Voice{}, false
	}
	for _, e := range m.voiceEntries() {
		if strings.EqualFold(e.Name, v) {
			return e, true
		}
	}
	return tts.Voice{}, false
}

// voiceDetail is the dim line under the Voice row: what the catalog knows
// about the selected voice, or the catalog's own notice.
func (m personasModel) voiceDetail() string {
	provider := m.effectiveStackTTSProvider()
	if m.voices.loading && liveVoiceCatalogProvider(provider) {
		return m.voices.note(provider)
	}
	if e, ok := m.selectedVoiceEntry(); ok {
		if detail := voiceEntryDetail(e); detail != "" {
			return detail
		}
	}
	return m.voices.notice
}

// formInnerWidth is the text budget inside a form box.
func (m personasModel) formInnerWidth() int { return m.formBoxWidth() - 4 }
