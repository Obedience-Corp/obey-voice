package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

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
