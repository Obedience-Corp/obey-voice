package tui

import (
	"fmt"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Where speech runs is host topology, not a persona setting: these rows write
// app config only, and Personas never learn the key (D13).
//
// The copy names computers, not hardware: "This computer" / "Another computer".
// A speech worker's GPU model belongs in a benchmark file, never in Settings.

// qwenPlaceItems renders the speech-place block. The address and token are
// always offered — you need somewhere to point at before switching — while the
// strictness row only appears once speech is actually running elsewhere.
func (m *settingsModel) qwenPlaceItems() []qwenOptionItem {
	lan := config.TTSPlaceOf(m.cfg) == config.TTSPlaceLAN

	where := "This computer"
	if lan {
		where = "Another computer"
		if host := speechHostLabel(m.cfg.TTSWorkerURL); host != "" {
			where += " · " + host
		}
	}
	items := []qwenOptionItem{
		{kind: qwenOptPlace, label: "Speech runs on — " + where + " · enter to switch"},
		{kind: qwenOptWorkerURL, label: "Speech computer address — " + speechURLLabel(m.cfg.TTSWorkerURL) + " · enter to edit"},
		{kind: qwenOptWorkerToken, label: "Speech computer token — " + speechTokenLabel(m.cfg) + " · enter to replace"},
	}
	if !lan {
		return items
	}
	strict := "OFF · speaks with Kokoro here when that computer is unreachable"
	if m.cfg.TTSLanStrict {
		strict = "ON · will not start a conversation without that computer"
	}
	return append(items, qwenOptionItem{kind: qwenOptLanStrict, label: "Require the speech computer — " + strict})
}

// speechURLLabel keeps the row readable when nothing is configured yet.
func speechURLLabel(raw string) string {
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		return trimmed
	}
	return "not set"
}

// speechHostLabel is the host:port part of the worker URL, for a compact row.
func speechHostLabel(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return trimmed
	}
	return parsed.Host
}

// speechTokenLabel reports whether a token exists and where it came from. The
// value itself is never rendered: Settings is read over shoulders and pasted
// into issues.
func speechTokenLabel(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.TTSWorkerToken) != "" {
		return "set"
	}
	if config.TTSWorkerTokenOf(cfg) != "" {
		return "set by " + config.SpeechTokenEnv
	}
	return "not set"
}

// selectQwenPlaceItem handles the speech-place rows. It reports false when the
// row is not one of them.
func (m *settingsModel) selectQwenPlaceItem(kind qwenOptionKind) (tea.Cmd, bool) {
	switch kind {
	case qwenOptPlace:
		return m.togglePlace(), true
	case qwenOptWorkerURL:
		return m.beginEdit(editWorkerURL, "Speech computer address", strings.TrimSpace(m.cfg.TTSWorkerURL)), true
	case qwenOptWorkerToken:
		// Never prefill: the stored token must not be readable from this screen.
		return m.beginEdit(editWorkerToken, "Speech computer token", ""), true
	case qwenOptLanStrict:
		return m.toggleLanStrict(), true
	}
	return nil, false
}

func (m *settingsModel) togglePlace() tea.Cmd {
	next := config.TTSPlaceLAN
	if config.TTSPlaceOf(m.cfg) == config.TTSPlaceLAN {
		next = config.TTSPlaceLocal
	}
	// Validate the whole topology before writing it: switching to another
	// computer with no address (or a plaintext one off this machine) would save
	// a config that cannot start a conversation.
	probe := *m.cfg
	probe.TTSPlace = next
	if err := config.ValidateTTSPlace(&probe); err != nil {
		m.message = fmt.Sprintf("Not switched: %v", err)
		return nil
	}
	if err := m.save("tts_place", next); err != nil {
		m.message = fmt.Sprintf("Failed to save where speech runs: %v", err)
		return nil
	}
	m.cfg.TTSPlace = next
	m.buildQwenItems()
	if next == config.TTSPlaceLAN {
		m.message = "Speech runs on another computer · " + speechApplyNote
		return nil
	}
	m.message = "Speech runs on this computer · " + speechApplyNote
	return nil
}

func (m *settingsModel) toggleLanStrict() tea.Cmd {
	next := !m.cfg.TTSLanStrict
	if err := m.save("tts_lan_strict", next); err != nil {
		m.message = fmt.Sprintf("Failed to save: %v", err)
		return nil
	}
	m.cfg.TTSLanStrict = next
	m.buildQwenItems()
	if next {
		m.message = "A conversation will not start unless the speech computer answers"
	} else {
		m.message = "An unreachable speech computer falls back to Kokoro on this computer"
	}
	return nil
}

// speechApplyNote is the honest scope of a place change: the live TTS set is
// rebuilt when Settings closes back into a conversation, and any new
// conversation picks it up.
const speechApplyNote = "applies when you return to the conversation"

// commitSpeechEdit writes an edited address or token.
func (m *settingsModel) commitSpeechEdit(kind settingsEditKind, value string) {
	value = strings.TrimSpace(value)
	switch kind {
	case editWorkerURL:
		// An address is only meaningful together with the place: check the pair
		// the user would end up with, so a bad URL is refused here rather than
		// at the start of the next conversation.
		probe := *m.cfg
		probe.TTSWorkerURL = value
		if value != "" || config.TTSPlaceOf(m.cfg) == config.TTSPlaceLAN {
			probe.TTSPlace = config.TTSPlaceLAN
			if err := config.ValidateTTSPlace(&probe); err != nil {
				m.message = fmt.Sprintf("Not saved: %v", err)
				return
			}
		}
		if err := m.save("tts_worker_url", value); err != nil {
			m.message = fmt.Sprintf("Failed to save the address: %v", err)
			return
		}
		m.cfg.TTSWorkerURL = value
		m.buildQwenItems()
		if value == "" {
			m.message = "Speech computer address cleared"
			return
		}
		m.message = "Speech computer address set to " + value

	case editWorkerToken:
		if err := m.save("tts_worker_token", value); err != nil {
			m.message = fmt.Sprintf("Failed to save the token: %v", err)
			return
		}
		m.cfg.TTSWorkerToken = value
		m.buildQwenItems()
		if value == "" {
			m.message = "Speech computer token cleared · " + config.SpeechTokenEnv + " still applies if set"
			return
		}
		m.message = "Speech computer token saved"
	}
}

// save routes through the model's injected writer so tests never touch disk.
func (m *settingsModel) save(key string, value any) error {
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	return save(key, value)
}
