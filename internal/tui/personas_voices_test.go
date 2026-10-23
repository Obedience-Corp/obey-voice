package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

var phononCatalog = []tts.Voice{
	{Name: "default", FriendlyName: "default"},
	{Name: "Captain Silas Blackwater", FriendlyName: "Captain Silas Blackwater", Locale: "characters", Description: "Gravelly pirate."},
	{Name: "Alfie", FriendlyName: "Alfie", Locale: "en-gb", Gender: "male", Age: "Young"},
	{Name: "Hugh", FriendlyName: "Hugh", Locale: "en-gb", Gender: "male", Age: "Adult"},
	{Name: "Harper", FriendlyName: "Harper", Locale: "en-us", Gender: "female", Age: "Adult", Description: "Clear and friendly."},
	{Name: "Zoey", FriendlyName: "Zoey", Locale: "en-us", Gender: "female", Age: "Young Adult", Description: "Bright and warm."},
	{Name: "Easton", FriendlyName: "Easton", Locale: "en-us", Gender: "male", Age: "Adult"},
	{Name: "Micah", FriendlyName: "Micah", Locale: "en-us", Gender: "male", Age: "Adult"},
	{Name: "Trevor", FriendlyName: "Trevor", Locale: "en-us", Gender: "male", Age: "Middle-age"},
	{Name: "Delia", FriendlyName: "Delia", Locale: "en-us", Gender: "female", Age: "Older Adult"},
}

// openaiSpeechForm opens the edit form on a persona that uses openai-speech,
// with the catalog loader stubbed so no HTTP happens and the test controls
// when the catalog lands.
func openaiSpeechForm(t *testing.T, voice string) (personasModel, *int) {
	t.Helper()
	cfg := &config.Config{ActivePersona: "obey", TTSProvider: "kokoro", TTSVoice: "af_heart"}
	m := newPersonas(cfg)
	loads := 0
	m.voices.load = func(context.Context, *config.Config, string) ([]tts.Voice, string, error) {
		loads++
		return phononCatalog, "", nil
	}
	m.directory.discover = func(context.Context) campdirs.Choices { return campdirs.Choices{} }
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "obey", DisplayName: "Obey",
			Brain: persona.Brain{Provider: "ollama", Model: "qwen3.8:latest"},
			TTS:   persona.TTS{Provider: config.OpenAISpeechProviderName, Voice: voice}}}, nil
	}
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "x", nil }
	m.reload()
	m.width, m.height = 120, 40
	m.cursor = 0
	m.beginEditAt(formRowVoice)
	return m, &loads
}

func landCatalog(t *testing.T, m personasModel) personasModel {
	t.Helper()
	if m.voices.cancel == nil && !m.voices.loading {
		t.Fatal("no catalog fetch in flight")
	}
	m.applyVoiceCatalog(personaVoicesMsg{seq: m.voices.seq, provider: config.OpenAISpeechProviderName, voices: phononCatalog})
	return m
}

func TestPersonaVoiceRowUsesTheLiveCatalogForOpenAISpeech(t *testing.T) {
	m, loads := openaiSpeechForm(t, "")
	// Until the fetch lands the row shows the static placeholder and says so.
	if got := strings.Join(m.stackVoiceList(), ","); got != "(default),default" {
		t.Fatalf("static list before fetch = %s", got)
	}
	if !strings.Contains(strings.Join(m.stackLines(), "\n"), "fetching the voice catalog") {
		t.Fatal("no fetching note under the Voice row")
	}
	m = landCatalog(t, m)
	list := m.stackVoiceList()
	if len(list) != len(phononCatalog)+1 || list[1] != "default" || list[len(list)-1] != "Delia" {
		t.Fatalf("live list = %v", list)
	}
	// ←/→ walks the live catalog and the row shows where in it we are.
	m.setFormRow(formRowVoice)
	for i := 0; i < 6; i++ {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	}
	if m.selectedVoice != "Zoey" {
		t.Fatalf("after six → selectedVoice = %q", m.selectedVoice)
	}
	screen := strings.Join(m.stackLines(), "\n")
	if !strings.Contains(screen, "Zoey") || !strings.Contains(screen, "6/10") {
		t.Fatalf("Voice row lacks the pick or its position:\n%s", screen)
	}
	if !strings.Contains(screen, "en-us · female · Young Adult · Bright and warm.") {
		t.Fatalf("detail line missing:\n%s", screen)
	}
	if *loads != 0 {
		t.Fatalf("stubbed loader was invoked synchronously %d times; it must run as a Cmd", *loads)
	}
}

func TestPersonaLanguageRowNarrowsTheVoiceRow(t *testing.T) {
	m, _ := openaiSpeechForm(t, "")
	if m.voiceLanguageUsable() {
		t.Fatal("language row usable before any catalog with locales")
	}
	m = landCatalog(t, m)
	if got := strings.Join(m.voiceLocales(), ","); got != "(all),characters,en-gb,en-us" {
		t.Fatalf("locales = %s", got)
	}
	m.setFormRow(formRowVoiceLanguage)
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if m.voiceLocale != "en-gb" {
		t.Fatalf("locale after two → = %q", m.voiceLocale)
	}
	// The unlocalised default stays; the other languages drop out.
	if got := strings.Join(m.stackVoiceList(), ","); got != "(default),default,Alfie,Hugh" {
		t.Fatalf("filtered list = %s", got)
	}
	screen := strings.Join(m.stackLines(), "\n")
	if !strings.Contains(screen, "‹ en-gb ›") {
		t.Fatalf("Language row does not show the filter:\n%s", screen)
	}
	m.setFormRow(formRowVoice)
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedVoice != "Alfie" {
		t.Fatalf("picked %q inside the en-gb filter", m.selectedVoice)
	}
	// ← from (all) wraps to the last language.
	m.voiceLocale = ""
	m.setFormRow(formRowVoiceLanguage)
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyLeft})
	if m.voiceLocale != "en-us" {
		t.Fatalf("← from (all) = %q", m.voiceLocale)
	}
}

func TestPersonaTTSChangeResetsTheLiveCatalogAndRefetches(t *testing.T) {
	m, _ := openaiSpeechForm(t, "Zoey")
	m = landCatalog(t, m)
	m.voiceLocale = "en-us"
	// Move the TTS row to Kokoro: live state and filter go, Kokoro's static
	// catalog takes over, the Phonon voice is dropped as not in it.
	m.setFormRow(formRowTTSProvider)
	for providerAt(stackTTSProviders(), m.ttsProviderIdx) != "kokoro" {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	}
	if m.voices.voices != nil || m.voiceLocale != "" || m.selectedVoice != "" {
		t.Fatalf("after moving to kokoro: live=%v locale=%q voice=%q", m.voices.voices != nil, m.voiceLocale, m.selectedVoice)
	}
	if !m.voiceLanguageUsable() {
		t.Fatal("Kokoro's catalog states locales; the Language row should be usable")
	}
	// Back to openai-speech: a fetch starts again.
	var cmd tea.Cmd
	for providerAt(stackTTSProviders(), m.ttsProviderIdx) != config.OpenAISpeechProviderName {
		m, cmd = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	}
	if cmd == nil || !m.voices.loading {
		t.Fatal("returning to openai-speech did not start a catalog fetch")
	}
}

func TestPersonaTypedServerVoiceSurvivesTheLiveCatalog(t *testing.T) {
	// ptts speaks any voice file in its folder, listed or not; a persona that
	// names one must not be reverted to default when the catalog lands.
	m, _ := openaiSpeechForm(t, "MyClone")
	m = landCatalog(t, m)
	if m.selectedVoice != "MyClone" {
		t.Fatalf("selectedVoice = %q after the catalog landed", m.selectedVoice)
	}
	list := m.stackVoiceList()
	if list[len(list)-1] != "MyClone" {
		t.Fatalf("typed voice not kept selectable: %v", list)
	}
}

func TestPersonaCatalogFailureKeepsTheStaticListAndSaysWhy(t *testing.T) {
	m, _ := openaiSpeechForm(t, "")
	m.applyVoiceCatalog(personaVoicesMsg{seq: m.voices.seq, provider: config.OpenAISpeechProviderName, err: errors.New("http://localhost:11434/v1 did not answer\nsecond line")})
	if got := strings.Join(m.stackVoiceList(), ","); got != "(default),default" {
		t.Fatalf("list after failure = %s", got)
	}
	screen := strings.Join(m.stackLines(), "\n")
	if !strings.Contains(screen, "voice catalog unavailable: http://localhost:11434/v1 did not answer") || strings.Contains(screen, "second line") {
		t.Fatalf("failure note wrong:\n%s", screen)
	}
	// A notice from the catalog itself (Ollama without voices) shows the same way.
	m2, _ := openaiSpeechForm(t, "")
	m2.applyVoiceCatalog(personaVoicesMsg{seq: m2.voices.seq, provider: config.OpenAISpeechProviderName,
		voices: []tts.Voice{{Name: "default", FriendlyName: "default"}}, notice: "Ollama 0.40.9 lists no voices for phonon"})
	if !strings.Contains(strings.Join(m2.stackLines(), "\n"), "Ollama 0.40.9 lists no voices") {
		t.Fatal("catalog notice not shown under the Voice row")
	}
	// A stale fetch (older seq) is ignored.
	m2.voices.seq++
	m2.applyVoiceCatalog(personaVoicesMsg{seq: m2.voices.seq - 1, provider: config.OpenAISpeechProviderName, voices: phononCatalog})
	if len(m2.voiceEntries()) != 1 {
		t.Fatal("stale catalog message was applied")
	}
}

func TestPersonaStaticProvidersNeverFetch(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}
	m := newPersonas(cfg)
	called := false
	m.voices.load = func(context.Context, *config.Config, string) ([]tts.Voice, string, error) {
		called = true
		return nil, "", nil
	}
	m.directory.discover = func(context.Context) campdirs.Choices { return campdirs.Choices{} }
	m.listPersonas = func() ([]*persona.Profile, error) { return nil, nil }
	m.reload()
	m.width, m.height = 100, 30
	cmd := m.beginCreate()
	if cmd == nil {
		t.Fatal("no batch on create")
	}
	if m.voices.loading || called {
		t.Fatal("a Kokoro form must not fetch a live catalog")
	}
	if !m.voiceLanguageUsable() {
		t.Fatal("Kokoro's static catalog states locales")
	}
}

// A fetch cancelled by moving the TTS row off openai-speech must not land on
// the Kokoro form as an error, and a late success must not let a Kokoro voice
// id ride back onto openai-speech.
func TestPersonaLateCatalogMessagesAreIgnoredAfterAProviderChange(t *testing.T) {
	m, _ := openaiSpeechForm(t, "")
	staleSeq := m.voices.seq
	m.setFormRow(formRowTTSProvider)
	for providerAt(stackTTSProviders(), m.ttsProviderIdx) != "kokoro" {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	}
	// The cancelled fetch reports its cancellation.
	m.applyVoiceCatalog(personaVoicesMsg{seq: staleSeq, provider: config.OpenAISpeechProviderName, err: context.Canceled})
	if screen := strings.Join(m.stackLines(), "\n"); strings.Contains(screen, "unavailable") {
		t.Fatalf("cancelled fetch surfaced on the Kokoro form:\n%s", screen)
	}
	// A success that beat the cancel lands too late as well.
	m.applyVoiceCatalog(personaVoicesMsg{seq: staleSeq, provider: config.OpenAISpeechProviderName, voices: phononCatalog})
	if m.voices.voices != nil {
		t.Fatal("late success was applied to a form that left openai-speech")
	}
	// Pick a Kokoro voice and return: the voice is not in Phonon's catalog and
	// must clear even while the new fetch is still in flight.
	m.setFormRow(formRowVoice)
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	kokoroVoice := m.selectedVoice
	if kokoroVoice == "" {
		t.Fatal("no Kokoro voice picked")
	}
	m.setFormRow(formRowTTSProvider)
	for providerAt(stackTTSProviders(), m.ttsProviderIdx) != config.OpenAISpeechProviderName {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	}
	if m.selectedVoice != "" {
		t.Fatalf("Kokoro voice %q survived the move to openai-speech", m.selectedVoice)
	}
}

func TestPersonaFormCloseRetiresTheCatalogFetch(t *testing.T) {
	m, _ := openaiSpeechForm(t, "")
	seq := m.voices.seq
	if !m.voices.loading {
		t.Fatal("no fetch in flight on open")
	}
	m.cancelForm()
	if m.voices.loading || m.voices.seq == seq {
		t.Fatalf("cancelForm left the fetch live: loading=%v seq %d→%d", m.voices.loading, seq, m.voices.seq)
	}
	m.applyVoiceCatalog(personaVoicesMsg{seq: seq, provider: config.OpenAISpeechProviderName, voices: phononCatalog})
	if m.voices.voices != nil {
		t.Fatal("a retired fetch was applied after the form closed")
	}
}
