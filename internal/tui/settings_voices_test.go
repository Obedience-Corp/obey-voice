package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// openaiSpeechSettings opens Settings on the Voice section with openai-speech
// as the engine and the catalog loader stubbed, the way the App opens it: the
// fetch is started and handed back as a Cmd the test runs when it chooses.
func openaiSpeechSettings(t *testing.T, voice string) (settingsModel, tea.Cmd, *int) {
	t.Helper()
	cfg := &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechVoice: voice}
	m := newSettings(cfg, nil)
	loads := 0
	m.voices.load = func(context.Context, *config.Config, string) ([]tts.Voice, string, error) {
		loads++
		return phononCatalog, "", nil
	}
	m.width, m.height = 120, 60
	cmd := m.loadVoiceCatalog()
	m.gotoSection(sectionVoice)
	return m, cmd, &loads
}

func voiceNames(voices []tts.Voice) string {
	names := make([]string, len(voices))
	for i, v := range voices {
		names[i] = v.Name
	}
	return strings.Join(names, ",")
}

func (m settingsModel) rowNamed(name string) (settingRow, bool) {
	for _, row := range m.sectionRows() {
		if row.name == name {
			return row, true
		}
	}
	return settingRow{}, false
}

func TestSettingsVoiceListReadsTheLiveOpenAISpeechCatalog(t *testing.T) {
	m, cmd, loads := openaiSpeechSettings(t, "")
	if cmd == nil || !m.voices.loading {
		t.Fatal("opening Settings on openai-speech started no catalog fetch")
	}
	if *loads != 0 {
		t.Fatalf("loader ran synchronously %d times; it must run as a Cmd", *loads)
	}
	// Until it lands: the static list, a note, and no Language filter.
	if got := voiceNames(m.shownVoiceItems()); got != "default" {
		t.Fatalf("static list before the fetch = %s", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "fetching the voice catalog…") {
		t.Fatalf("no fetching note in the Voice section:\n%s", view)
	}
	if _, ok := m.rowNamed("Language filter"); ok {
		t.Fatal("a Language filter over a list with no locales")
	}
	m.cursor = m.voiceLine(0)
	m, _ = m.Update(cmd())
	if *loads != 1 || m.voices.loading {
		t.Fatalf("after the Cmd: loads=%d loading=%v", *loads, m.voices.loading)
	}
	if got := voiceNames(m.shownVoiceItems()); got != voiceNames(phononCatalog) {
		t.Fatalf("live list = %s", got)
	}
	if name, ok := m.focusedVoice(); !ok || name != "default" {
		t.Fatalf("cursor left the voice it was on when the catalog landed: %q %v", name, ok)
	}
	view = stripANSI(m.View())
	for _, want := range []string{"✓ default", "Captain Silas Blackwater", "Harper", "Delia", "[(all)]", "characters", "en-gb", "en-us"} {
		if !strings.Contains(view, want) {
			t.Errorf("live Voice section missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "fetching") {
		t.Fatalf("fetching note survived the landing:\n%s", view)
	}
	// The highlighted voice's description replaces the row help.
	m.cursor = m.voiceLine(voiceIndex(m.shownVoiceItems(), "Zoey"))
	if got := stripANSI(m.detailLine()); !strings.Contains(got, "en-us · female · Young Adult · Bright and warm.") ||
		!strings.Contains(got, appliesUnlessPersona) {
		t.Fatalf("detail for Zoey = %q", got)
	}
	// enter chooses it under openai-speech's own key, and the engine list
	// names the new voice.
	saved := recordSaves(&m)
	m = pressSettingsKey(m, tea.KeyEnter)
	if saved["openai_speech_voice"] != "Zoey" {
		t.Fatalf("saved %v, want openai_speech_voice=Zoey", saved)
	}
	if !strings.Contains(stripANSI(m.View()), "✓ Zoey") {
		t.Fatal("✓ did not move to Zoey")
	}
	for _, it := range m.ttsItems {
		if it.provider == config.OpenAISpeechProviderName && !strings.HasSuffix(it.detail, "voice Zoey") {
			t.Fatalf("engine detail = %q, want it to name Zoey", it.detail)
		}
	}
}

// The catalog landing inserts the filter row and the live voices above
// whatever line the cursor is on; the cursor stays on that line, so enter
// does what the line said before the landing.
func TestSettingsCatalogLandingKeepsTheCursorOffTheVoiceList(t *testing.T) {
	m, cmd, _ := openaiSpeechSettings(t, "")
	saved := recordSaves(&m)
	m.cursor = lineOfRow(m, "Language", -1)
	if m.cursor < 0 {
		t.Fatal("no Language row below the voice list")
	}
	m, _ = m.Update(cmd())
	if row, _, _ := m.focused(); row.name != "Language" {
		t.Fatalf("after the landing the cursor is on %q, want Language", row.name)
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if len(saved) != 0 {
		t.Fatalf("enter on the Language row after the landing saved %v", saved)
	}

	// The same through the remembered cursor of a section that is not shown.
	m2, cmd2, _ := openaiSpeechSettings(t, "")
	saved2 := recordSaves(&m2)
	m2.cursor = lineOfRow(m2, "Language", -1)
	m2.gotoSection(sectionQwen)
	m2, _ = m2.Update(cmd2())
	m2.gotoSection(sectionVoice)
	if row, _, _ := m2.focused(); row.name != "Language" {
		t.Fatalf("returning to Voice after the landing lands on %q, want Language", row.name)
	}
	m2 = pressSettingsKey(m2, tea.KeyEnter)
	if len(saved2) != 0 {
		t.Fatalf("enter after returning saved %v", saved2)
	}
}

// The fetch runs on its own goroutine while Update keeps writing the config;
// it must read a copy. Run with -race.
func TestSettingsCatalogFetchReadsItsOwnConfigCopy(t *testing.T) {
	cfg := &config.Config{TTSProvider: config.OpenAISpeechProviderName}
	m := newSettings(cfg, nil)
	recordSaves(&m)
	seen := make(chan *config.Config, 1)
	m.voices.load = func(_ context.Context, got *config.Config, _ string) ([]tts.Voice, string, error) {
		_ = got.OpenAISpeechVoice + got.TTSProvider + got.OpenAISpeechURL
		seen <- got
		return phononCatalog, "", nil
	}
	cmd := m.loadVoiceCatalog()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	m.selectVoice(tts.Voice{Name: "Zoey"})
	msg := <-done
	if got := <-seen; got == cfg {
		t.Fatal("the loader was handed the shared config")
	}
	m, _ = m.Update(msg)
	if cfg.OpenAISpeechVoice != "Zoey" || m.voices.landed(config.OpenAISpeechProviderName) == nil {
		t.Fatalf("voice %q, landed %v", cfg.OpenAISpeechVoice, m.voices.landed(config.OpenAISpeechProviderName) != nil)
	}
}

func TestSettingsConfiguredVoiceTheCatalogMissesStaysListed(t *testing.T) {
	m, cmd, _ := openaiSpeechSettings(t, "MyClone")
	m, _ = m.Update(cmd())
	shown := m.shownVoiceItems()
	if shown[len(shown)-1].Name != "MyClone" {
		t.Fatalf("configured voice not kept: %s", voiceNames(shown))
	}
	row, _ := m.rowNamed("Voice")
	if row.current != len(shown)-1 {
		t.Fatalf("✓ on option %d, want the kept voice", row.current)
	}
}

func TestSettingsCatalogFailureKeepsTheStaticListAndSaysWhy(t *testing.T) {
	m, _, _ := openaiSpeechSettings(t, "")
	m, _ = m.Update(settingsVoicesMsg{seq: m.voices.seq, provider: config.OpenAISpeechProviderName,
		err: errors.New("http://localhost:11434/v1 did not answer\nsecond line")})
	if got := voiceNames(m.shownVoiceItems()); got != "default" {
		t.Fatalf("list after failure = %s", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "voice catalog unavailable: http://localhost:11434/v1 did not answer") || strings.Contains(view, "second line") {
		t.Fatalf("failure note wrong:\n%s", view)
	}

	// Ollama without voices: the catalog's own notice, the same way.
	m2, _, _ := openaiSpeechSettings(t, "")
	m2, _ = m2.Update(settingsVoicesMsg{seq: m2.voices.seq, provider: config.OpenAISpeechProviderName,
		voices: []tts.Voice{{Name: "default", FriendlyName: "default"}}, notice: "Ollama 0.40.9 lists no voices for phonon"})
	if !strings.Contains(stripANSI(m2.View()), "Ollama 0.40.9 lists no voices for phonon") {
		t.Fatal("catalog notice not shown in the Voice section")
	}

	// An answer to another fetch (an earlier Settings visit) is ignored.
	m3, _, _ := openaiSpeechSettings(t, "")
	m3, _ = m3.Update(settingsVoicesMsg{seq: m3.voices.seq - 1, provider: config.OpenAISpeechProviderName, voices: phononCatalog})
	m3, _ = m3.Update(settingsVoicesMsg{seq: m3.voices.seq, provider: "kokoro", voices: phononCatalog})
	if !m3.voices.loading || len(m3.voiceItems) != 1 {
		t.Fatal("a stale or foreign catalog message was applied")
	}
}

func TestSettingsLanguageFilterNarrowsTheVoiceList(t *testing.T) {
	m, cmd, _ := openaiSpeechSettings(t, "Zoey")
	m, _ = m.Update(cmd())
	saved := recordSaves(&m)
	m.cursor = lineOfRow(m, "Language filter", -1)
	if m.cursor < 0 {
		t.Fatal("no Language filter row after the catalog landed")
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.voiceLocale != "en-gb" {
		t.Fatalf("filter after two enters = %q", m.voiceLocale)
	}
	// The unlocalised default stays, and so does the ✓ voice from en-us.
	if got := voiceNames(m.shownVoiceItems()); got != "default,Alfie,Hugh,Zoey" {
		t.Fatalf("filtered list = %s", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "[en-gb]") || !strings.Contains(view, "✓ Zoey") || strings.Contains(view, "Harper") {
		t.Fatalf("filtered view wrong:\n%s", view)
	}
	m = typeIntoSettings(m, "-")
	if m.voiceLocale != "characters" {
		t.Fatalf("- stepped to %q, want characters", m.voiceLocale)
	}
	if len(saved) != 0 {
		t.Fatalf("the filter wrote config: %v", saved)
	}
	// ←/→ stay section navigation on the filter row.
	m = pressSettingsKey(m, tea.KeyRight)
	if m.section != sectionQwen || m.voiceLocale != "characters" {
		t.Fatalf("→ on the filter: section %v filter %q", m.section, m.voiceLocale)
	}
	m = pressSettingsKey(m, tea.KeyLeft)
	if row, _, _ := m.focused(); row.name != "Language filter" {
		t.Fatalf("returning to Voice landed on %q, want the filter row", row.name)
	}
}

func TestSettingsEngineChangeDropsTheLiveCatalog(t *testing.T) {
	m, cmd, _ := openaiSpeechSettings(t, "Zoey")
	m, _ = m.Update(cmd())
	m.voiceLocale = "en-us"
	recordSaves(&m)
	choose := func(provider string) tea.Cmd {
		for i, it := range m.ttsItems {
			if it.provider == provider {
				m.cursor = lineOf(m, "tts_provider", i)
			}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return cmd
	}
	inFlight := m.loadVoiceCatalog()
	if inFlight != nil {
		t.Fatal("a landed catalog was fetched again")
	}
	m.voices.voices = nil
	inFlight = m.loadVoiceCatalog()
	if cmd := choose("kokoro"); cmd != nil {
		t.Fatal("choosing Kokoro started a fetch")
	}
	// The fetch that was in flight answers after the move: it is outdated.
	m, _ = m.Update(inFlight())
	if m.voices.voices != nil || voiceIndex(m.voiceItems, "Harper") >= 0 {
		t.Fatal("an outdated openai-speech answer was applied after the move to Kokoro")
	}
	if m.voices.landed(config.OpenAISpeechProviderName) != nil || m.voices.loading || m.voiceLocale != "" {
		t.Fatalf("live state survived the move to Kokoro: landed=%v loading=%v filter=%q",
			m.voices.landed(config.OpenAISpeechProviderName) != nil, m.voices.loading, m.voiceLocale)
	}
	if idx := voiceIndex(m.voiceItems, "af_heart"); idx < 0 || voiceIndex(m.voiceItems, "Harper") >= 0 {
		t.Fatalf("Kokoro's static catalog did not take over: %s", voiceNames(m.voiceItems))
	}
	if _, ok := m.rowNamed("Language filter"); !ok {
		t.Fatal("Kokoro's catalog states locales; the filter should be offered")
	}
	if strings.Contains(stripANSI(m.View()), "fetching") {
		t.Fatal("Kokoro shows a catalog note")
	}
	// Back to openai-speech: a new fetch, and the static list until it lands.
	cmd = choose(config.OpenAISpeechProviderName)
	if cmd == nil || !m.voices.loading {
		t.Fatal("returning to openai-speech did not start a catalog fetch")
	}
	if got := voiceNames(m.voiceItems); got != "Zoey,default" {
		t.Fatalf("static list on return = %s", got)
	}
}

func TestSettingsStaticEnginesNeverFetchAndKokoroFilters(t *testing.T) {
	for _, provider := range []string{"kokoro", "qwen3-tts"} {
		m := newSettings(&config.Config{TTSProvider: provider, TTSVoice: "af_heart", ModelsDir: t.TempDir()}, nil)
		called := false
		m.voices.load = func(context.Context, *config.Config, string) ([]tts.Voice, string, error) {
			called = true
			return nil, "", nil
		}
		if cmd := m.loadVoiceCatalog(); cmd != nil || m.voices.loading || called {
			t.Fatalf("%s started a live catalog fetch", provider)
		}
	}
	m := newSettings(&config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}, nil)
	m.gotoSection(sectionVoice)
	row, ok := m.rowNamed("Language filter")
	if !ok {
		t.Fatal("Kokoro has no Language filter")
	}
	if got := strings.Join(row.options, ","); got != "(all),en-GB,en-US" {
		t.Fatalf("Kokoro locales = %s", got)
	}
	m.cursor = lineOfRow(m, "Language filter", -1)
	m = typeIntoSettings(m, "-")
	if m.voiceLocale != "en-US" {
		t.Fatalf("- from (all) = %q, want en-US", m.voiceLocale)
	}
	for _, v := range m.shownVoiceItems() {
		if v.Locale != "en-US" {
			t.Fatalf("%s (%s) shown under en-US", v.Name, v.Locale)
		}
	}
	m = typeIntoSettings(m, "-")
	if got := m.shownVoiceItems(); len(got) == 0 || got[len(got)-1].Locale != "en-GB" || voiceIndex(got, "af_heart") < 0 || voiceIndex(got, "af_bella") >= 0 {
		t.Fatalf("en-GB filter lost the ✓ voice or kept en-US: %s", voiceNames(got))
	}
}

// A catalog with many languages does not fit the filter row at 80 columns:
// the options wrap under it, every one on screen, and ↑/↓ skip the wrapped
// lines.
func TestSettingsLanguageFilterWrapsSoEveryLanguageStaysOnScreen(t *testing.T) {
	locales := []string{"de-de", "en-gb", "en-us", "es-es", "fr-fr", "it-it", "ja-jp", "ko-kr", "pt-br", "zh-cn"}
	var voices []tts.Voice
	for _, loc := range locales {
		voices = append(voices, tts.Voice{Name: "v-" + loc, Locale: loc})
	}
	for _, width := range []int{70, 80} {
		m, _, _ := openaiSpeechSettings(t, "")
		m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		m, _ = m.Update(settingsVoicesMsg{seq: m.voices.seq, provider: config.OpenAISpeechProviderName, voices: voices})
		m.cursor = lineOfRow(m, "Language filter", -1)
		for i := 0; i < 5; i++ {
			m = pressSettingsKey(m, tea.KeyEnter)
		}
		if m.voiceLocale != "fr-fr" {
			t.Fatalf("width %d: five enters = %q", width, m.voiceLocale)
		}
		view := stripANSI(m.View())
		if !strings.Contains(view, "[fr-fr]") {
			t.Fatalf("width %d: current language cut on its row:\n%s", width, view)
		}
		for _, loc := range locales {
			if !strings.Contains(view, " "+loc+" ") && !strings.Contains(view, "["+loc+"]") {
				t.Fatalf("width %d: %s is not on screen:\n%s", width, loc, view)
			}
		}
		lines := strings.Split(view, "\n")
		for _, line := range lines {
			if strings.Contains(line, "-de ") || strings.Contains(line, "-cn ") || strings.Contains(line, "fr-fr") {
				if strings.HasSuffix(strings.TrimRight(line, " "), "…") {
					t.Fatalf("width %d: a filter line was truncated: %q", width, line)
				}
			}
		}
		if len(lines) != 40 {
			t.Fatalf("width %d: view is %d rows, want 40", width, len(lines))
		}
		m = pressSettingsKey(m, tea.KeyDown)
		if name, ok := m.focusedVoice(); !ok || name != "v-fr-fr" {
			t.Fatalf("width %d: ↓ from the filter landed on %q, want the first voice", width, name)
		}
	}
}

// A resize that changes how the filter wraps keeps the cursor on its voice.
func TestSettingsResizeKeepsTheCursorOnItsLine(t *testing.T) {
	var voices []tts.Voice
	for _, loc := range []string{"de-de", "en-gb", "en-us", "es-es", "fr-fr", "it-it", "ja-jp", "ko-kr", "pt-br", "zh-cn"} {
		voices = append(voices, tts.Voice{Name: "v-" + loc, Locale: loc})
	}
	m, _, _ := openaiSpeechSettings(t, "")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m, _ = m.Update(settingsVoicesMsg{seq: m.voices.seq, provider: config.OpenAISpeechProviderName, voices: voices})
	m.cursor = m.voiceLine(voiceIndex(m.shownVoiceItems(), "v-ja-jp"))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	if name, _ := m.focusedVoice(); name != "v-ja-jp" {
		t.Fatalf("after narrowing the cursor is on %q, want v-ja-jp", name)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	if name, _ := m.focusedVoice(); name != "v-ja-jp" {
		t.Fatalf("after widening the cursor is on %q, want v-ja-jp", name)
	}
}

// Opening Settings starts the fetch (the Cmd is not run here, so nothing is
// dialled); leaving Settings cancels it.
func TestOpeningSettingsFetchesTheCatalogAndLeavingCancelsIt(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	app := App{cfg: &config.Config{TTSProvider: config.OpenAISpeechProviderName}, screen: screenLauncher}
	model, cmd := app.Update(switchScreenMsg(screenSettings))
	app = model.(App)
	if cmd == nil || !app.settings.voices.loading || app.settings.voices.cancel == nil {
		t.Fatalf("opening Settings on openai-speech started no fetch (loading=%v)", app.settings.voices.loading)
	}
	seq := app.settings.voices.seq
	model, _ = app.Update(settingsDoneMsg{})
	app = model.(App)
	if app.settings.voices.cancel != nil || app.settings.voices.loading || app.settings.voices.seq == seq {
		t.Fatal("leaving Settings left the catalog fetch running or current")
	}
}
