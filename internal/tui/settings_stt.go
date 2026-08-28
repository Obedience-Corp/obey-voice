package tui

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// The STT tab picks which recognizer hears you. Rows come from the factory's
// provider table, normalized to canonical names: a build that adds a provider
// gets a row without a change here, and a compound alias like sherpa-streaming
// is never written back into config.
//
// Copy names what the user gets, not the hardware it runs on.

// sttOptionItem is one recognizer row.
type sttOptionItem struct {
	// provider is the canonical name written to stt_provider.
	provider string
	// mode is the stt_mode written with it, so a stale mode from a previous
	// provider can never survive into an invalid pair.
	mode  string
	label string
}

// sttProviderBlurbs is this screen's own copy for the providers it knows.
// Anything else falls back to the factory description, so a new provider is
// listed with its own words rather than silently unlabeled.
var sttProviderBlurbs = map[string]string{
	config.STTProviderParakeet:      "fastest and most accurate here; ends on your pause",
	config.STTProviderSherpa:        "Whisper; slower, and it sometimes invents subtitle text",
	config.STTProviderWhisperCPP:    "Whisper through the whisper.cpp command line",
	config.STTProviderTranscribeCPP: "Parakeet through the transcribe.cpp command line",
}

func (m *settingsModel) buildSTTItems() {
	m.sttItems = nil
	if m.cfg == nil {
		return
	}
	lookPath := m.binaryLookPath()
	modelsDir := config.ModelsDirFrom(m.cfg)
	// stt_mode is part of the current state: resolving the provider alone
	// would demote a canonical sherpa+streaming pair to offline on re-pick.
	current, err := config.NormalizeSTTWithMode(m.cfg.STTProvider, m.cfg.STTMode)
	if err != nil {
		current, _ = config.NormalizeSTT(m.cfg.STTProvider)
	}

	for _, provider := range canonicalSTTProviders() {
		mode := sttModeFor(provider, current)
		label := provider
		if blurb := sttProviderBlurbs[provider]; blurb != "" {
			label += " — " + blurb
		} else if description := sttProviderDescription(provider); description != "" {
			label += " — " + description
		}
		if mode != config.STTModeOffline && mode != "" {
			label += " · " + mode
		}
		if gap := config.STTEnvironmentGap(provider, m.cfg, modelsDir, lookPath); gap.Missing() {
			label += " · " + gap.Detail
		}
		m.sttItems = append(m.sttItems, sttOptionItem{provider: provider, mode: mode, label: label})
	}
}

// canonicalSTTProviders is the factory's table folded onto the names config
// actually stores: sherpa-parakeet and parakeet are one recognizer, and
// sherpa-offline is sherpa. Order follows the factory table.
func canonicalSTTProviders() []string {
	var out []string
	for _, spec := range stt.Providers() {
		norm, ok := config.NormalizeSTT(spec.Name)
		if !ok || slices.Contains(out, norm.Provider) {
			continue
		}
		out = append(out, norm.Provider)
	}
	return out
}

// sttProviderDescription is the factory's own text for a provider this screen
// has no copy for. The first alias that normalizes to it wins.
func sttProviderDescription(provider string) string {
	for _, spec := range stt.Providers() {
		if norm, ok := config.NormalizeSTT(spec.Name); ok && norm.Provider == provider {
			return spec.Description
		}
	}
	return ""
}

// sttModeFor keeps the mode the user already has when the new provider still
// supports it (sherpa streaming survives a round trip through this picker) and
// otherwise takes that provider's own first mode.
func sttModeFor(provider string, current config.NormalizedSTT) string {
	modes := config.STTModes(provider)
	if len(modes) == 0 {
		return ""
	}
	if current.Provider == provider && slices.Contains(modes, current.Mode) {
		return current.Mode
	}
	return modes[0]
}

// activeSTTProvider is the recognizer this config resolves to today.
func activeSTTProvider(cfg *config.Config) string {
	norm, ok := config.NormalizeSTT(cfg.STTProvider)
	if !ok {
		return ""
	}
	return norm.Provider
}

// sttConfiguredAliasNote names the stored value when it is not the canonical
// one — an install still pinned to sherpa-offline is reading a row labelled
// sherpa, and should be told why.
func sttConfiguredAliasNote(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	norm, ok := config.NormalizeSTT(cfg.STTProvider)
	if !ok {
		if configured := strings.TrimSpace(cfg.STTProvider); configured != "" {
			return fmt.Sprintf("stt_provider %q is not a recognizer this build has; pick one below", configured)
		}
		return ""
	}
	if norm.Alias == "" || norm.Alias == norm.Provider {
		return ""
	}
	return fmt.Sprintf("configured as %q, which is %s · %s", norm.Alias, norm.Provider, norm.Mode)
}

// binaryLookPath resolves the external binaries a picker reports on — the
// recognizers here, the transcribe.cpp CLI behind the Meeting tab's diarizer.
// Tests inject a fake.
func (m *settingsModel) binaryLookPath() func(string) (string, error) {
	if m.lookPath != nil {
		return m.lookPath
	}
	return exec.LookPath
}

func (m *settingsModel) selectSTTItem() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.sttItems) {
		return nil
	}
	item := m.sttItems[m.cursor]

	// Provider and mode are written as a pair, so the result is always a pair
	// this build can resolve. A refusal here would mean the table itself is
	// wrong, which is worth failing loudly rather than saving.
	if _, err := config.NormalizeSTTWithMode(item.provider, item.mode); err != nil {
		m.message = fmt.Sprintf("Not saved: %v", err)
		return nil
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("stt_provider", item.provider); err != nil {
		m.message = fmt.Sprintf("Failed to save the recognizer: %v", err)
		return nil
	}
	m.cfg.STTProvider = item.provider
	// The mode is written explicitly every time: a leftover stt_mode from a
	// previous provider (streaming, say) would otherwise make the new pair
	// invalid at the next conversation.
	if err := save("stt_mode", item.mode); err != nil {
		m.message = fmt.Sprintf("Recognizer set to %s, but stt_mode could not be written: %v", item.provider, err)
		return nil
	}
	m.cfg.STTMode = item.mode
	m.buildSTTItems()

	// Honest scope: ReloadVoice rebuilds TTS only. STT is constructed with the
	// microphone when a conversation starts, and this one keeps the recognizer
	// it opened with.
	m.message = fmt.Sprintf("Speech recognition set to %s · %s", item.provider, sttApplyNote)
	if gap := config.STTEnvironmentGap(item.provider, m.cfg, config.ModelsDirFrom(m.cfg), m.binaryLookPath()); gap.Missing() {
		m.message += fmt.Sprintf(" · %s is not ready on this computer: %s — %s", item.provider, gap.Detail, gap.Remediation)
	}
	return nil
}

// sttApplyNote is the truth about when a recognizer change takes effect.
const sttApplyNote = "applies to a new conversation (this one keeps the recognizer it started with)"
