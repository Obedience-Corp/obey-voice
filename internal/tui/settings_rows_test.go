package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
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

// lineOf is the line of row key's option, or of the row itself when option
// is -1; -1 when there is no such line.
func lineOf(m settingsModel, key string, option int) int {
	rows := m.sectionRows()
	for i, l := range linesFor(rows) {
		if l.focusable() && rows[l.row].key == key && l.option == option {
			return i
		}
	}
	return -1
}

// lineOfRow is lineOf by row name, for rows that write no key.
func lineOfRow(m settingsModel, name string, option int) int {
	rows := m.sectionRows()
	for i, l := range linesFor(rows) {
		if l.focusable() && rows[l.row].name == name && l.option == option {
			return i
		}
	}
	return -1
}

func brainTestProviders() []discovery.ProviderInfo {
	return []discovery.ProviderInfo{
		{Name: "ollama", Available: true, Models: []string{"qwen3:8b", "llama3.1:8b", "gemma3:12b"}},
		{Name: "grok", Available: false},
		{Name: "claude", Available: true},
	}
}

// Leaving a section and coming back lands on the line you left.
func TestSettingsSectionRemembersItsCursor(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, nil)
	m.gotoSection(sectionTools)
	for i := 0; i < 3; i++ {
		m = pressSettingsKey(m, tea.KeyDown)
	}
	m = pressSettingsKey(m, tea.KeyRight)
	if m.section != sectionVoice {
		t.Fatalf("after →: section %v, want Voice", m.section)
	}
	m = pressSettingsKey(m, tea.KeyLeft)
	if m.section != sectionTools || m.cursor != 3 {
		t.Fatalf("after ←: section %v cursor %d, want Tools line 3", m.section, m.cursor)
	}
}

// ←/→ and h/l move between sections and wrap; tab still does, for the
// habit. Switching writes nothing.
func TestSettingsArrowsSwitchSections(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, brainTestProviders())
	saved := recordSaves(&m)
	m = pressSettingsKey(m, tea.KeyLeft)
	if m.section != sectionMeeting {
		t.Fatalf("← from Brain = %v, want Meeting (wrapping)", m.section)
	}
	m = pressSettingsKey(m, tea.KeyRight)
	if m.section != sectionBrain {
		t.Fatalf("→ from Meeting = %v, want Brain (wrapping)", m.section)
	}
	m = typeIntoSettings(m, "l")
	if m.section != sectionWorker {
		t.Fatalf("l from Brain = %v, want Worker", m.section)
	}
	m = typeIntoSettings(m, "h")
	if m.section != sectionBrain {
		t.Fatalf("h from Worker = %v, want Brain", m.section)
	}
	m = pressSettingsKey(m, tea.KeyTab)
	if m.section != sectionWorker {
		t.Fatalf("tab from Brain = %v, want Worker", m.section)
	}
	if len(saved) != 0 {
		t.Fatalf("switching sections wrote %v", saved)
	}
}

// A digit jumps straight to that section.
func TestSettingsDigitsJumpToSections(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m = typeIntoSettings(m, "4")
	if m.section != sectionVoice {
		t.Fatalf("4 = %v, want Voice", m.section)
	}
	m = typeIntoSettings(m, "9")
	if m.section != sectionMeeting {
		t.Fatalf("9 = %v, want Meeting", m.section)
	}
	m = typeIntoSettings(m, "1")
	if m.section != sectionBrain {
		t.Fatalf("1 = %v, want Brain", m.section)
	}
}

// Brain shows every provider and every model on one screen with the current
// ones marked, beside a sidebar that lists every section.
func TestSettingsBrainListsProvidersAndModelsInline(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama", OllamaModel: "llama3.1:8b"}, brainTestProviders())
	m.width, m.height = 80, 24
	view := stripANSI(m.View())
	for _, want := range []string{
		"Provider", "✓ ollama", "grok (not available)", "claude",
		"Model", "qwen3:8b", "✓ llama3.1:8b", "gemma3:12b",
		"1 Brain", "4 Voice", "9 Meeting", "│",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Brain view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "‹") {
		t.Fatalf("Brain draws a list as a stepper:\n%s", view)
	}
}

// A section that leads with a list opens on its current option; ↑/↓ then
// skip the headings and blanks between lists.
func TestSettingsOpensOnTheCurrentOptionAndSkipsHeadings(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "claude"}, brainTestProviders())
	row, line, ok := m.focused()
	if !ok || row.key != "brain_provider" || line.option != 2 {
		t.Fatalf("opened on %q option %d, want the current provider claude", row.name, line.option)
	}
	m = pressSettingsKey(m, tea.KeyDown)
	row, line, ok = m.focused()
	if !ok || row.name != "Model" || line.option != 0 {
		t.Fatalf("↓ from the last provider landed on %q option %d, want the first model", row.name, line.option)
	}
	m = pressSettingsKey(m, tea.KeyUp)
	row, line, ok = m.focused()
	if !ok || row.key != "brain_provider" || line.option != 2 {
		t.Fatalf("↑ from the first model landed on %q option %d, want claude", row.name, line.option)
	}
}

// enter chooses the option under the cursor.
func TestSettingsEnterChoosesTheOptionUnderTheCursor(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro"}
	m := newSettings(cfg, nil)
	saved := recordSaves(&m)
	m.gotoSection(sectionVoice)
	m.voiceItems = []tts.Voice{{Name: "af_heart"}, {Name: "af_bella"}}
	key := config.TTSVoiceKeyFor("kokoro")
	m.cursor = lineOf(m, key, 1)
	if m.cursor < 0 {
		t.Fatal("no line for af_bella")
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if saved[key] != "af_bella" {
		t.Fatalf("saved %v, want the voice under the cursor (af_bella)", saved[key])
	}
}

// Moving through lists writes nothing: an option is chosen, never stepped
// to, and moving over the engine whose choice installs a package does not
// install it.
func TestSettingsMovingNeverWrites(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro", ModelsDir: t.TempDir()}
	m := newSettings(cfg, nil)
	saved := recordSaves(&m)
	m.gotoSection(sectionVoice)
	m.voiceItems = []tts.Voice{{Name: "af_heart"}, {Name: "af_bella"}}
	for i := 0; i < 12; i++ {
		m = pressSettingsKey(m, tea.KeyDown)
	}
	for i := 0; i < 12; i++ {
		m = pressSettingsKey(m, tea.KeyUp)
	}
	if len(saved) != 0 || m.qwenInstalling || cfg.TTSProvider != "kokoro" {
		t.Fatalf("moving wrote %v, installing %v, provider %q", saved, m.qwenInstalling, cfg.TTSProvider)
	}
}

// The engine that needs a download is listed dim, and choosing it is what
// installs it.
func TestSettingsChoosingAMutedEngineInstallsIt(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro", ModelsDir: t.TempDir()}
	m := newSettings(cfg, nil)
	recordSaves(&m)
	m.gotoSection(sectionVoice)
	idx := -1
	for i, it := range m.ttsItems {
		if it.provider == managedqwen.ProviderName {
			idx = i
		}
	}
	m.cursor = lineOf(m, "tts_provider", idx)
	row, _, ok := m.focused()
	if !ok || idx < 0 || !row.muted[idx] {
		t.Fatalf("qwen3-tts is not a muted engine option (idx %d)", idx)
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if !m.qwenInstalling {
		t.Fatal("enter on the engine did not start the install")
	}
	if m.qwenInstallCancel != nil {
		m.qwenInstallCancel()
	}
}

// A choice row shows all its options on the row; enter or space move it on,
// - moves it back.
func TestSettingsChoiceRowCyclesInPlace(t *testing.T) {
	m := newSettings(&config.Config{TalkHotkeyEnabled: true, TalkHotkeyMode: "hold"}, nil)
	saved := recordSaves(&m)
	m.gotoSection(sectionTools)
	m.cursor = lineOf(m, "talk_hotkey_mode", -1)
	m.width, m.height = 80, 24
	view := stripANSI(m.View())
	for _, want := range []string{"[hold]", "toggle", "automatic"} {
		if !strings.Contains(view, want) {
			t.Fatalf("choice row missing %q:\n%s", want, view)
		}
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.cfg.TalkHotkeyMode != "toggle" {
		t.Fatalf("enter moved to %q, want toggle", m.cfg.TalkHotkeyMode)
	}
	m = pressSettingsKey(m, tea.KeySpace)
	if m.cfg.TalkHotkeyMode != "automatic" {
		t.Fatalf("space moved to %q, want automatic", m.cfg.TalkHotkeyMode)
	}
	m = typeIntoSettings(m, "-")
	if m.cfg.TalkHotkeyMode != "toggle" || saved["talk_hotkey_mode"] != "toggle" {
		t.Fatalf("- moved to %q (saved %v), want toggle", m.cfg.TalkHotkeyMode, saved["talk_hotkey_mode"])
	}
}

// d does nothing: removal is a line, never a bare letter.
func TestSettingsDIsUnbound(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m := newSettings(&config.Config{}, nil)
	m.gotoSection(sectionSpeakers)
	m.enrolledProfiles = []speaker.Profile{{Name: "Lance", Owner: true}, {Name: "Alex"}}
	guest := lineOfRow(m, "Alex", -1)
	if guest < 0 {
		t.Fatal("no guest line for Alex")
	}
	m.cursor = guest
	m = typeIntoSettings(m, "d")
	if m.confirmRemoveVoice != "" || m.cursor != guest || m.section != sectionSpeakers {
		t.Fatalf("d did something: confirm %q cursor %d section %v", m.confirmRemoveVoice, m.cursor, m.section)
	}
}

// The footer offers p on a voice, not on an engine.
func TestSettingsFooterOffersPreviewOnAVoice(t *testing.T) {
	m := newSettings(&config.Config{TTSProvider: "kokoro"}, nil)
	m.gotoSection(sectionVoice)
	m.voiceItems = []tts.Voice{{Name: "af_heart"}}
	m.cursor = lineOf(m, "tts_provider", 0)
	if strings.Contains(m.footerHelp(), "p preview") {
		t.Fatalf("footer offers p on an engine: %q", m.footerHelp())
	}
	m.cursor = lineOf(m, config.TTSVoiceKeyFor("kokoro"), 0)
	if !strings.Contains(m.footerHelp(), "p preview") {
		t.Fatalf("footer hides p on a voice: %q", m.footerHelp())
	}
}

// A status message is read by moving on; the next line's detail returns.
func TestSettingsCursorMoveClearsTheMessage(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, nil)
	recordSaves(&m)
	m.gotoSection(sectionTools)
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

// Choosing a brain lands on Worker when none is chosen. The brain writer is
// the real one, so it gets a throwaway config root.
func TestSettingsChoosingABrainLandsOnWorker(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "grok"}
	m := newSettings(cfg, workerTestProviders())
	m.cursor = lineOf(m, "brain_provider", 0)
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.section != sectionWorker || cfg.BrainProvider != "ollama" {
		t.Fatalf("choosing ollama: section %v brain %q, want Worker and ollama", m.section, cfg.BrainProvider)
	}
}

// A terminal too narrow for the sidebar gets a section strip instead, and
// every line still fits.
func TestSettingsNarrowTerminalDropsTheSidebar(t *testing.T) {
	m := newSettings(&config.Config{BrainProvider: "ollama"}, brainTestProviders())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	view := stripANSI(m.View())
	if strings.Contains(view, "│") {
		t.Fatalf("narrow terminal still draws the sidebar:\n%s", view)
	}
	if !strings.Contains(view, "‹ Brain ›") {
		t.Fatalf("narrow terminal has no section strip:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 20 {
		t.Fatalf("view has %d rows, want 20:\n%s", len(lines), view)
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 50 {
			t.Fatalf("line wider than the terminal: %q", line)
		}
	}
}

// When a list scrolls, its heading stays above its first option.
func TestSettingsHeadingScrollsWithItsFirstOption(t *testing.T) {
	models := make([]string, 30)
	for i := range models {
		models[i] = "m" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	providers := []discovery.ProviderInfo{{Name: "ollama", Available: true, Models: models}}
	m := newSettings(&config.Config{BrainProvider: "ollama"}, providers)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	for i := 0; i < 10; i++ {
		m = pressSettingsKey(m, tea.KeyDown)
	}
	if view := stripANSI(m.View()); strings.Contains(view, "Model ·") {
		t.Fatalf("heading still on screen ten models down:\n%s", view)
	}
	for i := 0; i < 9; i++ {
		m = pressSettingsKey(m, tea.KeyUp)
	}
	row, line, _ := m.focused()
	if row.name != "Model" || line.option != 0 {
		t.Fatalf("cursor on %q option %d, want the first model", row.name, line.option)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Model ·") || !strings.Contains(view, models[0]) {
		t.Fatalf("heading is not above the first model:\n%s", view)
	}
}
