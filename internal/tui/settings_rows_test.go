package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func pressSettingsKey(m settingsModel, k tea.KeyType) settingsModel {
	m, _ = m.Update(tea.KeyMsg{Type: k})
	return m
}

func typeIntoSettings(m settingsModel, text string) settingsModel {
	for _, r := range text {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func recordSaves(m *settingsModel) map[string]any {
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	return saved
}

// Leaving a section and coming back lands on the row you left.
func TestSettingsSectionRemembersItsCursor(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, nil)
	m.section = sectionTools
	for i := 0; i < 3; i++ {
		m = pressSettingsKey(m, tea.KeyDown)
	}
	m = pressSettingsKey(m, tea.KeyTab)
	if m.section != sectionTTS || m.cursor != 0 {
		t.Fatalf("after tab: section %v cursor %d, want TTS row 0", m.section, m.cursor)
	}
	m = pressSettingsKey(m, tea.KeyShiftTab)
	if m.section != sectionTools || m.cursor != 3 {
		t.Fatalf("after shift+tab: section %v cursor %d, want Tools row 3", m.section, m.cursor)
	}
}

// enter opens the list, typing narrows it, enter chooses what is highlighted.
func TestSettingsPickerFiltersAndChooses(t *testing.T) {
	m := newSettings(&config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}, nil)
	saved := recordSaves(&m)
	m.section = sectionTools
	m.cursor = toolRowTalkMode

	m = pressSettingsKey(m, tea.KeyEnter)
	if !m.picker.active {
		t.Fatal("enter on a pick row did not open the list")
	}
	m = typeIntoSettings(m, "auto")
	if len(m.picker.matches) != 1 {
		t.Fatalf("filter 'auto' kept %d options, want 1", len(m.picker.matches))
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.picker.active {
		t.Fatal("choosing did not close the list")
	}
	if saved["talk_hotkey_mode"] != "automatic" || m.cfg.TalkHotkeyMode != "automatic" {
		t.Fatalf("saved %v cfg %q, want automatic", saved["talk_hotkey_mode"], m.cfg.TalkHotkeyMode)
	}
}

// Backing out of the list writes nothing, however far the cursor moved.
func TestSettingsPickerEscWritesNothing(t *testing.T) {
	m := newSettings(&config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}, nil)
	saved := recordSaves(&m)
	m.section = sectionTools
	m.cursor = toolRowTalkMode

	m = pressSettingsKey(m, tea.KeyEnter)
	m = typeIntoSettings(m, "tog")
	m = pressSettingsKey(m, tea.KeyDown)
	m = pressSettingsKey(m, tea.KeyEsc)
	if m.picker.active {
		t.Fatal("esc did not close the list")
	}
	if len(saved) != 0 || m.cfg.TalkHotkeyMode != "hold" {
		t.Fatalf("esc wrote %v, cfg %q; want nothing written and hold", saved, m.cfg.TalkHotkeyMode)
	}
}

// →/← step over a backend that is not installed instead of dead-ending on it.
func TestSettingsStepSkipsAnUnavailableWorker(t *testing.T) {
	providers := []discovery.ProviderInfo{
		{Name: "ollama", Available: true},
		{Name: "grok", Available: false},
		{Name: "claude", Available: true},
	}
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama"}
	m := newSettings(cfg, providers)
	saved := recordSaves(&m)
	m.section = sectionWorker
	m.cursor = 0

	m = pressSettingsKey(m, tea.KeyRight)
	if cfg.VoiceWorkerProvider != "claude" || saved["voice_worker_provider"] != "claude" {
		t.Fatalf("→ from ollama chose %q (saved %v), want claude past the unavailable grok", cfg.VoiceWorkerProvider, saved["voice_worker_provider"])
	}
	if strings.Contains(m.message, "not available") {
		t.Fatalf("stepping tripped on the unavailable backend: %q", m.message)
	}
}

// A keypress must not be what starts a large download: → skips the engine
// whose choice would install it, and the list is where that choice is made.
func TestSettingsStepDoesNotStartAnInstall(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro", ModelsDir: t.TempDir()}
	m := newSettings(cfg, nil)
	recordSaves(&m)
	m.section = sectionTTS
	m.cursor = 0

	m = pressSettingsKey(m, tea.KeyRight)
	if m.qwenInstalling {
		t.Fatal("→ on the engine row started the Qwen package install")
	}
	if cfg.TTSProvider == "qwen3-tts" || cfg.TTSProvider == "kokoro" {
		t.Fatalf("→ landed on %q, want the next engine that needs no install", cfg.TTSProvider)
	}
}

// The list chooses what it showed, even if the model's list was rebuilt
// underneath it (a Qwen install finishing while the Voice list is open).
func TestSettingsPickerChoosesWhatItShowed(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro"}
	m := newSettings(cfg, nil)
	saved := recordSaves(&m)
	m.section = sectionVoice
	m.voiceItems = []tts.Voice{{Name: "af_heart"}, {Name: "af_bella"}}

	m = pressSettingsKey(m, tea.KeyEnter)
	m = pressSettingsKey(m, tea.KeyDown) // af_bella highlighted
	m.voiceItems = []tts.Voice{{Name: "x_one"}, {Name: "x_two"}}
	m = pressSettingsKey(m, tea.KeyEnter)
	if saved["tts_voice"] != "af_bella" {
		t.Fatalf("saved %v, want the voice the list showed (af_bella)", saved["tts_voice"])
	}
}

// d does nothing: removal is a row, never a bare letter.
func TestSettingsDIsUnbound(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m := newSettings(&config.Config{}, nil)
	m.section = sectionSpeakers
	m.enrolledProfiles = []speaker.Profile{{Name: "Lance", Owner: true}, {Name: "Alex"}}
	guest := -1
	for i, row := range m.sectionRows() {
		if row.name == "Alex" {
			guest = i
		}
	}
	if guest < 0 {
		t.Fatal("no guest row for Alex")
	}
	m.cursor = guest
	m = typeIntoSettings(m, "d")
	if m.confirmRemoveVoice != "" || m.picker.active || m.cursor != guest {
		t.Fatalf("d did something: confirm %q picker %v cursor %d", m.confirmRemoveVoice, m.picker.active, m.cursor)
	}
}

// The footer names p only when there is a current voice for it to play.
func TestSettingsFooterOffersPreviewOnlyForACurrentVoice(t *testing.T) {
	m := newSettings(&config.Config{TTSProvider: "kokoro"}, nil)
	m.section = sectionVoice
	m.voiceItems = []tts.Voice{{Name: "af_heart"}}
	if strings.Contains(m.footerHelp(), "p preview") {
		t.Fatalf("footer offers p with no current voice: %q", m.footerHelp())
	}
	m.cfg.TTSVoice = "af_heart"
	if !strings.Contains(m.footerHelp(), "p preview") {
		t.Fatalf("footer hides p with a current voice: %q", m.footerHelp())
	}
}

// A status message is read by moving on; the next row's detail line returns.
func TestSettingsCursorMoveClearsTheMessage(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, nil)
	recordSaves(&m)
	m.section = sectionTools
	m.cursor = toolRowLocalTools
	m.width, m.height = 80, 24

	m = pressSettingsKey(m, tea.KeySpace)
	if m.message == "" {
		t.Fatal("toggling wrote no message")
	}
	m = pressSettingsKey(m, tea.KeyDown)
	if m.message != "" {
		t.Fatalf("message survived a cursor move: %q", m.message)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Ollama only") && !strings.Contains(view, "skills") {
		t.Fatalf("detail line for the next row is missing:\n%s", view)
	}
}

// → on the Brain row changes the brain and stays put; only choosing from the
// list lands on Worker. The brain writer is the real one, so it gets a
// throwaway config root.
func TestSettingsStepOnBrainStaysOnBrain(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "grok"}
	m := newSettings(cfg, workerTestProviders())
	m.section = sectionProvider
	m.cursor = 0

	m = pressSettingsKey(m, tea.KeyRight)
	if m.section != sectionProvider {
		t.Fatalf("→ on Brain moved to section %v, want to stay on Brain", m.section)
	}
	if cfg.BrainProvider == "grok" {
		t.Fatal("→ on Brain did not change the brain")
	}
	if strings.Contains(m.message, "choose the worker") {
		t.Fatalf("stepping asked for a worker: %q", m.message)
	}

	// The list still hands off to Worker when nothing is chosen there.
	m = pressSettingsKey(m, tea.KeyEnter)
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.section != sectionWorker {
		t.Fatalf("choosing from the list landed on %v, want Worker", m.section)
	}
}

// When every other option is one ←/→ must skip, the row says so instead of
// re-saving what is already set.
func TestSettingsStepSaysWhenThereIsNowhereToGo(t *testing.T) {
	providers := []discovery.ProviderInfo{
		{Name: "ollama", Available: true},
		{Name: "grok", Available: false},
	}
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama"}
	m := newSettings(cfg, providers)
	saved := recordSaves(&m)
	m.section = sectionWorker
	m.cursor = 0

	m = pressSettingsKey(m, tea.KeyRight)
	if len(saved) != 0 {
		t.Fatalf("→ with nowhere to go wrote %v", saved)
	}
	if !strings.Contains(m.message, "No other") {
		t.Fatalf("message = %q, want it to say there is nowhere to step", m.message)
	}
}
