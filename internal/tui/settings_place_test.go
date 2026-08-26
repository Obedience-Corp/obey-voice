package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// placeModel is a settings model whose writes are recorded instead of saved.
func placeModel(t *testing.T, cfg *config.Config) (*settingsModel, map[string]any) {
	t.Helper()
	if cfg.ModelsDir == "" {
		cfg.ModelsDir = t.TempDir()
	}
	if cfg.TTSProvider == "" {
		cfg.TTSProvider = "qwen3-tts"
	}
	saved := map[string]any{}
	m := &settingsModel{cfg: cfg, saveConfig: func(key string, value any) error {
		saved[key] = value
		return nil
	}}
	m.buildQwenItems()
	return m, saved
}

func placeRow(t *testing.T, m *settingsModel, kind qwenOptionKind) (int, string) {
	t.Helper()
	for i, item := range m.qwenItems {
		if item.kind == kind {
			return i, item.label
		}
	}
	t.Fatalf("row %v is not in the Qwen tab: %+v", kind, m.qwenItems)
	return 0, ""
}

func selectRow(t *testing.T, m *settingsModel, kind qwenOptionKind) {
	t.Helper()
	idx, _ := placeRow(t, m, kind)
	m.cursor = idx
	m.selectQwenItem()
}

func TestPlaceRowsDefaultToThisComputer(t *testing.T) {
	m, _ := placeModel(t, &config.Config{})

	_, place := placeRow(t, m, qwenOptPlace)
	if !strings.Contains(place, "This computer") {
		t.Fatalf("place row = %q", place)
	}
	_, address := placeRow(t, m, qwenOptWorkerURL)
	if !strings.Contains(address, "not set") {
		t.Fatalf("address row = %q", address)
	}
	for _, item := range m.qwenItems {
		if item.kind == qwenOptLanStrict {
			t.Fatalf("the strictness row belongs to lan only: %q", item.label)
		}
	}
}

// Switching computers with nowhere to point at must surface the validation
// error, not save a config that cannot start a conversation.
func TestTogglePlaceRefusesLanWithoutAnAddress(t *testing.T) {
	m, saved := placeModel(t, &config.Config{})

	selectRow(t, m, qwenOptPlace)

	if _, written := saved["tts_place"]; written {
		t.Fatalf("place was saved anyway: %v", saved)
	}
	if m.cfg.TTSPlace != "" {
		t.Fatalf("live config changed: %q", m.cfg.TTSPlace)
	}
	if !strings.Contains(m.message, "tts_worker_url") {
		t.Fatalf("message = %q, want the validation error", m.message)
	}
}

func TestTogglePlaceRoundTrip(t *testing.T) {
	m, saved := placeModel(t, &config.Config{TTSWorkerURL: "https://studio:7263"})

	selectRow(t, m, qwenOptPlace)
	if saved["tts_place"] != config.TTSPlaceLAN || m.cfg.TTSPlace != config.TTSPlaceLAN {
		t.Fatalf("saved=%v cfg=%q", saved, m.cfg.TTSPlace)
	}
	_, place := placeRow(t, m, qwenOptPlace)
	if !strings.Contains(place, "Another computer") || !strings.Contains(place, "studio:7263") {
		t.Fatalf("place row = %q", place)
	}
	if _, strict := placeRow(t, m, qwenOptLanStrict); strict == "" {
		t.Fatal("the strictness row must appear once speech runs elsewhere")
	}

	selectRow(t, m, qwenOptPlace)
	if saved["tts_place"] != config.TTSPlaceLocal || m.cfg.TTSPlace != config.TTSPlaceLocal {
		t.Fatalf("saved=%v cfg=%q", saved, m.cfg.TTSPlace)
	}
}

func TestToggleLanStrict(t *testing.T) {
	m, saved := placeModel(t, &config.Config{TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"})

	selectRow(t, m, qwenOptLanStrict)
	if saved["tts_lan_strict"] != true || !m.cfg.TTSLanStrict {
		t.Fatalf("saved=%v cfg=%v", saved, m.cfg.TTSLanStrict)
	}
	_, strict := placeRow(t, m, qwenOptLanStrict)
	if !strings.Contains(strict, "ON") {
		t.Fatalf("strict row = %q", strict)
	}

	selectRow(t, m, qwenOptLanStrict)
	if saved["tts_lan_strict"] != false || m.cfg.TTSLanStrict {
		t.Fatalf("saved=%v cfg=%v", saved, m.cfg.TTSLanStrict)
	}
}

// The token is a shared secret: Settings reports that one exists and nothing
// more, on the row, in the editor, and in the rendered screen.
func TestTokenIsNeverRendered(t *testing.T) {
	const secret = "s3cret-token-value"
	m, _ := placeModel(t, &config.Config{TTSWorkerToken: secret})

	_, row := placeRow(t, m, qwenOptWorkerToken)
	if strings.Contains(row, secret) {
		t.Fatalf("token row leaked the value: %q", row)
	}
	if !strings.Contains(row, "set") {
		t.Fatalf("token row = %q, want it to report a stored token", row)
	}

	selectRow(t, m, qwenOptWorkerToken)
	if m.edit.kind != editWorkerToken {
		t.Fatalf("edit kind = %v", m.edit.kind)
	}
	if m.edit.input.Value() != "" {
		t.Fatalf("the editor prefilled the stored token: %q", m.edit.input.Value())
	}
	if line := m.editorLine(); strings.Contains(line, secret) {
		t.Fatalf("editor line leaked the value: %q", line)
	}

	typed := typeInto(*m, "abc")
	if line := typed.editorLine(); strings.Contains(line, "abc") {
		t.Fatalf("the editor echoed a typed secret: %q", line)
	}
	if view := typed.View(); strings.Contains(view, secret) || strings.Contains(view, "abc") {
		t.Fatalf("Settings rendered a secret")
	}
}

func TestTokenRowReportsTheEnvironmentOverride(t *testing.T) {
	t.Setenv(config.SpeechTokenEnv, "from-env")
	m, _ := placeModel(t, &config.Config{})

	_, row := placeRow(t, m, qwenOptWorkerToken)
	if !strings.Contains(row, config.SpeechTokenEnv) {
		t.Fatalf("token row = %q, want it to name the environment override", row)
	}
	if strings.Contains(row, "from-env") {
		t.Fatalf("token row leaked the value: %q", row)
	}
}

func TestEditWorkerTokenSaves(t *testing.T) {
	m, saved := placeModel(t, &config.Config{})

	selectRow(t, m, qwenOptWorkerToken)
	next := commitTyped(*m, "brand-new-token")
	if saved["tts_worker_token"] != "brand-new-token" {
		t.Fatalf("saved=%v", saved)
	}
	if next.edit.kind != editNone {
		t.Fatal("the editor must close on enter")
	}
}

func TestEditWorkerURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		place   string
		typed   string
		wantErr string
		want    string
	}{
		{name: "plaintext off box", typed: "http://192.168.1.1:7263", wantErr: "plaintext"},
		{name: "not a url", typed: "tcp://studio:7263", wantErr: "https"},
		{name: "tls host", typed: "https://studio:7263", want: "https://studio:7263"},
		{name: "loopback plaintext", typed: "http://127.0.0.1:7263", want: "http://127.0.0.1:7263"},
		{name: "cleared while local", typed: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{TTSPlace: tc.place}
			m, saved := placeModel(t, cfg)

			selectRow(t, m, qwenOptWorkerURL)
			if m.edit.kind != editWorkerURL {
				t.Fatalf("edit kind = %v", m.edit.kind)
			}
			next := commitTyped(*m, tc.typed)

			if tc.wantErr != "" {
				if _, written := saved["tts_worker_url"]; written {
					t.Fatalf("a rejected address was saved: %v", saved)
				}
				if !strings.Contains(next.message, tc.wantErr) {
					t.Fatalf("message = %q, want %q", next.message, tc.wantErr)
				}
				return
			}
			if saved["tts_worker_url"] != tc.want {
				t.Fatalf("saved=%v want %q", saved, tc.want)
			}
			if next.cfg.TTSWorkerURL != tc.want {
				t.Fatalf("live config = %q", next.cfg.TTSWorkerURL)
			}
		})
	}
}

// An empty address is only harmless while speech runs here.
func TestEditWorkerURLRefusesClearingWhileOnAnotherComputer(t *testing.T) {
	m, saved := placeModel(t, &config.Config{TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"})

	selectRow(t, m, qwenOptWorkerURL)
	next := commitTyped(*m, "")
	if _, written := saved["tts_worker_url"]; written {
		t.Fatalf("cleared the only address a lan session has: %v", saved)
	}
	if !strings.Contains(next.message, "tts_worker_url") {
		t.Fatalf("message = %q", next.message)
	}
}

func TestEditorOwnsKeysAndCancels(t *testing.T) {
	m, saved := placeModel(t, &config.Config{})
	before := m.section

	selectRow(t, m, qwenOptWorkerURL)
	// "q" and "h" are Settings navigation keys; inside the editor they are text.
	next := typeInto(*m, "qh")
	if next.section != before {
		t.Fatalf("typing navigated Settings: section=%v", next.section)
	}
	if next.edit.input.Value() != "qh" {
		t.Fatalf("value = %q", next.edit.input.Value())
	}

	next, _ = next.updateEditor(tea.KeyMsg{Type: tea.KeyEsc})
	if next.edit.kind != editNone {
		t.Fatal("esc must close the editor")
	}
	if len(saved) != 0 {
		t.Fatalf("cancelling wrote config: %v", saved)
	}
}

// Place is app topology; the picker writes app keys only, never a persona
// field (D13).
func TestPlaceRowsWriteOnlyAppKeys(t *testing.T) {
	m, saved := placeModel(t, &config.Config{TTSWorkerURL: "https://studio:7263"})

	selectRow(t, m, qwenOptPlace)
	selectRow(t, m, qwenOptLanStrict)
	selectRow(t, m, qwenOptWorkerToken)
	commitTyped(*m, "token")

	allowed := map[string]bool{"tts_place": true, "tts_lan_strict": true, "tts_worker_token": true, "tts_worker_url": true}
	for key := range saved {
		if !allowed[key] {
			t.Fatalf("unexpected key written: %s", key)
		}
	}
}

// Settings copy names computers, never the hardware in them (D12).
func TestPlaceCopyNamesComputersNotHardware(t *testing.T) {
	m, _ := placeModel(t, &config.Config{TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"})

	var joined string
	for _, item := range m.qwenItems {
		joined += item.label + "\n"
	}
	for _, banned := range []string{"RTX", "5060", "NVIDIA", "CUDA", "Metal", "GPU"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("Settings copy names hardware (%q): %s", banned, joined)
		}
	}
}

func typeInto(m settingsModel, text string) settingsModel {
	for _, r := range text {
		m, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// commitTyped replaces whatever the editor was opened with — an empty string
// is a deliberate "clear this" — and presses enter.
func commitTyped(m settingsModel, text string) settingsModel {
	m.edit.input.SetValue(text)
	m, _ = m.updateEditor(tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

// Switching computers has to look like a voice change to the app, or Settings
// would close back into a conversation still speaking through the old
// connection: voiceSettingsState is what triggers ReloadVoice → NewTTSSet →
// LiveTTSManager.Install.
func TestVoiceSettingsStateTracksPlace(t *testing.T) {
	base := &config.Config{TTSProvider: "qwen3-tts", TTSWorkerURL: "https://studio:7263"}
	local := voiceSettingsState(base)

	for _, mutate := range []func(*config.Config){
		func(c *config.Config) { c.TTSPlace = config.TTSPlaceLAN },
		func(c *config.Config) { c.TTSWorkerURL = "https://other:7263" },
		func(c *config.Config) { c.TTSWorkerToken = "rotated" },
		func(c *config.Config) { c.TTSLanStrict = true },
	} {
		next := *base
		mutate(&next)
		if voiceSettingsState(&next) == local {
			t.Fatalf("a speech-place change did not register as a voice change: %+v", next)
		}
	}

	// An explicit "local" is the same state as the unset default.
	explicit := *base
	explicit.TTSPlace = config.TTSPlaceLocal
	if voiceSettingsState(&explicit) != local {
		t.Fatal("explicit local must not look like a change")
	}
}

// The chip reports the surprising state. A single-computer install says
// nothing extra; speech on another computer is named.
func TestTTSBadgeShowsPlaceOnlyWhenElsewhere(t *testing.T) {
	local := &config.Config{TTSProvider: "qwen3-tts", ModelsDir: t.TempDir()}
	if badge := ttsBadgeLabel(local); strings.Contains(badge, "lan") {
		t.Fatalf("badge = %q, want no place noise for this computer", badge)
	}

	lan := &config.Config{TTSProvider: "qwen3-tts", ModelsDir: t.TempDir(), TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"}
	badge := ttsBadgeLabel(lan)
	if !strings.Contains(badge, "lan studio:7263") {
		t.Fatalf("badge = %q, want the speech computer named", badge)
	}
	// A host with no local package must not be told its package is missing.
	if strings.Contains(badge, "not installed") {
		t.Fatalf("badge = %q, want no local-package complaint on a lan host", badge)
	}
	if !strings.Contains(badge, "speech worker studio:7263") {
		t.Fatalf("badge = %q, want the model slot to name the worker", badge)
	}
}
