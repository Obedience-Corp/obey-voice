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

// registerTestVoice writes one voice into the store the way the register
// command does, without the encoder: the pickers read voice.json only.
func registerTestVoice(t *testing.T, modelsDir, name string) {
	t.Helper()
	meta := tts.Audio8VoiceMeta{
		Name:             name,
		Transcript:       "the reference line",
		ModelFingerprint: "audio8-tts-preview-0.1b-int8-v1",
		SampleRate:       44100,
		Provenance:       tts.Audio8VoiceProvenance{Kind: tts.Audio8VoiceRecorded, Consent: true},
	}
	codes := make([][]int64, 10)
	for i := range codes {
		codes[i] = []int64{int64(i), 1, 2}
	}
	if err := tts.SaveAudio8Voice(modelsDir, meta, codes); err != nil {
		t.Fatal(err)
	}
}

// Selecting audio8 must list the packaged voice rather than crash or fall
// through to kokoro's catalog, and every registered voice beside it.
func TestSettingsVoiceListForAudio8(t *testing.T) {
	modelsDir := t.TempDir()
	m := newSettings(&config.Config{TTSProvider: "audio8", ModelsDir: modelsDir}, nil)
	m.buildVoiceItems()
	if len(m.voiceItems) != 1 || m.voiceItems[0].Name != "reference" {
		t.Fatalf("voice items = %+v, want the single audio8 reference voice", m.voiceItems)
	}

	registerTestVoice(t, modelsDir, "alice")
	registerTestVoice(t, modelsDir, "bob")
	m = newSettings(&config.Config{TTSProvider: "audio8", ModelsDir: modelsDir}, nil)
	m.buildVoiceItems()
	var names []string
	for _, v := range m.voiceItems {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "reference,alice,bob" {
		t.Fatalf("voice items = %v, want the packaged reference plus both registered voices", names)
	}
}

// A registered voice must be selectable in Settings, and the selection must
// land on audio8's own key: tts_voice is a Kokoro enum that would refuse it.
func TestSettingsSelectingAnAudio8VoiceWritesItsOwnKey(t *testing.T) {
	modelsDir := t.TempDir()
	registerTestVoice(t, modelsDir, "alice")
	cfg := &config.Config{TTSProvider: "audio8", ModelsDir: modelsDir}
	m := newSettings(cfg, nil)
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.section = sectionVoice
	m.buildVoiceItems()
	idx := slices.IndexFunc(m.voiceItems, func(v tts.Voice) bool { return v.Name == "alice" })
	if idx < 0 {
		t.Fatal("the registered voice is not selectable")
	}
	m.selectVoice(m.voiceItems[idx])

	if saved["audio8_voice"] != "alice" {
		t.Errorf("saved = %v, want audio8_voice=alice", saved)
	}
	if _, wrote := saved["tts_voice"]; wrote {
		t.Error("Settings wrote the audio8 voice into tts_voice, which only Kokoro reads")
	}
	if cfg.Audio8Voice != "alice" {
		t.Errorf("cfg.Audio8Voice = %q, want alice", cfg.Audio8Voice)
	}
}

func TestStackVoiceCatalogForAudio8(t *testing.T) {
	modelsDir := t.TempDir()
	cfg := &config.Config{ModelsDir: modelsDir}
	if got := stackVoiceCatalog(cfg, "audio8"); len(got) != 1 || got[0] != "reference" {
		t.Fatalf("stackVoiceCatalog(audio8) = %v, want [reference]", got)
	}
	registerTestVoice(t, modelsDir, "alice")
	got := stackVoiceCatalog(cfg, "audio8")
	if strings.Join(got, ",") != "reference,alice" {
		t.Fatalf("stackVoiceCatalog(audio8) = %v, want [reference alice]; a persona could not select a registered voice", got)
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
		if got := stackVoiceCatalog(nil, provider); len(got) != len(kokoro) {
			t.Fatalf("stackVoiceCatalog(%q) = %d voices, want %d", provider, len(got), len(kokoro))
		}
	}
}

// The audiobook form composes a `--voice` flag for the render it launches, so
// under audio8 it used to compose `--voice af_heart` — the same root cause as
// the render blocker, one screen further out.
func TestAudiobookVoiceForAudio8(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "an untouched config gets the packaged reference",
			cfg:  config.Config{TTSProvider: "audio8", TTSVoice: "af_heart"},
			want: config.Audio8PackagedVoice,
		},
		{
			name: "a registered voice",
			cfg:  config.Config{TTSProvider: "audio8", TTSVoice: "af_heart", Audio8Voice: "mine"},
			want: "mine",
		},
		{name: "kokoro is unchanged", cfg: config.Config{TTSProvider: "kokoro", TTSVoice: "af_bella"}, want: "af_bella"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newAudiobook(&tt.cfg).voice; got != tt.want {
				t.Errorf("audiobook voice = %q, want %q (it becomes --voice)", got, tt.want)
			}
		})
	}
}

// The launcher badge reported `voice af_heart` for an audio8 stack whatever
// audio8_voice said.
func TestLauncherVoiceLabelForAudio8(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "an untouched config names the packaged reference",
			cfg:  config.Config{TTSProvider: "audio8", TTSVoice: "af_heart"},
			want: "voice " + config.Audio8PackagedVoice,
		},
		{
			name: "a registered voice",
			cfg:  config.Config{TTSProvider: "audio8", Audio8Voice: "mine"},
			want: "voice mine",
		},
		{name: "kokoro default", cfg: config.Config{TTSProvider: "kokoro"}, want: "voice af_heart"},
		{name: "kokoro set", cfg: config.Config{TTSProvider: "kokoro", TTSVoice: "af_sky"}, want: "voice af_sky"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ttsEffectiveVoiceLabel(&tt.cfg); got != tt.want {
				t.Errorf("badge = %q, want %q", got, tt.want)
			}
		})
	}
}

// Provenance is not gender: putting it in Gender made `voices --gender male`
// drop every registered voice and the Settings row read "alice  recording /".
func TestRegisteredVoicesCarryKindNotGender(t *testing.T) {
	modelsDir := t.TempDir()
	registerTestVoice(t, modelsDir, "alice")
	voices, err := tts.StaticVoicesFor(&config.Config{ModelsDir: modelsDir}, "audio8", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range voices {
		if v.Name != "alice" {
			continue
		}
		found = true
		if v.Gender != "" {
			t.Errorf("registered voice reports gender %q; a reference recording has none", v.Gender)
		}
		if v.Kind != tts.Audio8VoiceRecorded {
			t.Errorf("Kind = %q, want %q", v.Kind, tts.Audio8VoiceRecorded)
		}
	}
	if !found {
		t.Fatal("the registered voice is missing from the catalog")
	}

	// And a gender filter no longer matches provenance.
	filtered, err := tts.StaticVoicesFor(&config.Config{ModelsDir: modelsDir}, "audio8", "", "recording")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range filtered {
		if v.Name == "alice" {
			t.Error("--gender recording matched a registered voice; provenance is not a gender")
		}
	}
}
