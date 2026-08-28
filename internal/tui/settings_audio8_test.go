package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// Settings builds its TTS list from tts.Providers(), so audio8 appears there
// with no per-provider wiring. This pins that: the row exists, carries the
// catalog description (where the experimental marker lives), and reports the
// missing export instead of implying it can speak.
func TestSettingsTTSListIncludesAudio8(t *testing.T) {
	m := newSettings(&config.Config{TTSProvider: "kokoro", ModelsDir: t.TempDir()}, nil)
	m.buildTTSItems()

	idx := slices.IndexFunc(m.ttsItems, func(item ttsSettingItem) bool { return item.provider == "audio8" })
	if idx < 0 {
		t.Fatalf("TTS items = %+v, missing audio8", m.ttsItems)
	}
	detail := strings.ToLower(m.ttsItems[idx].detail)
	if !strings.Contains(detail, "experimental") {
		t.Fatalf("audio8 detail = %q, want the experimental marker from the catalog", m.ttsItems[idx].detail)
	}
	if !strings.Contains(detail, "not installed") {
		t.Fatalf("audio8 detail = %q, want the not-installed guidance for an empty models dir", m.ttsItems[idx].detail)
	}
}

func TestSettingsTTSDetailForAnInstalledAudio8(t *testing.T) {
	modelsDir := t.TempDir()
	dir := filepath.Join(modelsDir, "audio8-tts")
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "tokenizer", "tokenizer.json"),
		filepath.Join(dir, "runtime_manifest.json"),
	} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := newSettings(&config.Config{TTSProvider: "kokoro", ModelsDir: modelsDir}, nil)
	m.buildTTSItems()

	idx := slices.IndexFunc(m.ttsItems, func(item ttsSettingItem) bool { return item.provider == "audio8" })
	if idx < 0 {
		t.Fatalf("TTS items = %+v, missing audio8", m.ttsItems)
	}
	if strings.Contains(m.ttsItems[idx].detail, "not installed") {
		t.Fatalf("audio8 detail = %q, want no not-installed note once the export is present", m.ttsItems[idx].detail)
	}
}

// Selecting audio8 must list its one packaged voice rather than crash or fall
// through to kokoro's catalog.
func TestSettingsVoiceListForAudio8(t *testing.T) {
	m := newSettings(&config.Config{TTSProvider: "audio8", ModelsDir: t.TempDir()}, nil)
	m.buildVoiceItems()

	if len(m.voiceItems) != 1 || m.voiceItems[0].Name != "reference" {
		t.Fatalf("voice items = %+v, want the single audio8 reference voice", m.voiceItems)
	}
}

func TestStackVoiceCatalogForAudio8(t *testing.T) {
	got := stackVoiceCatalog("audio8")
	if len(got) != 1 || got[0] != "reference" {
		t.Fatalf("stackVoiceCatalog(audio8) = %v, want [reference]", got)
	}
}

// The persona form's provider row is built from the same catalog.
func TestStackTTSProvidersIncludesAudio8(t *testing.T) {
	if !slices.Contains(stackTTSProviders(), "audio8") {
		t.Fatalf("stackTTSProviders() = %v, missing audio8", stackTTSProviders())
	}
}

// kokoro must keep its own catalog when audio8 shares the switch arm.
func TestStackVoiceCatalogStillHandlesKokoro(t *testing.T) {
	kokoro, err := tts.StaticVoices("kokoro", "", "")
	if err != nil {
		t.Fatalf("StaticVoices: %v", err)
	}
	for _, provider := range []string{"", "kokoro"} {
		if got := stackVoiceCatalog(provider); len(got) != len(kokoro) {
			t.Fatalf("stackVoiceCatalog(%q) = %d voices, want %d", provider, len(got), len(kokoro))
		}
	}
}
