package persona

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestCreateQwenVoiceRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name     string
		legacy   string
		qwen     string
		want     string
		invalid  bool
		external bool
	}{
		{name: "kokoro fallback", legacy: "af_heart"},
		{name: "alternate kokoro fallback", legacy: "af_sky"},
		{name: "unknown fallback", legacy: "not-a-qwen-voice"},
		{name: "empty fallback"},
		{name: "whitespace unset", legacy: "af_heart", qwen: "  "},
		{name: "legacy preset", legacy: " Ryan ", want: "Ryan"},
		{name: "legacy shipped clone", legacy: "VeronicaDesk", want: "VeronicaDesk"},
		{name: "explicit preset", legacy: "af_heart", qwen: "Uncle_Fu", want: "Uncle_Fu"},
		{name: "explicit shipped clone", legacy: "af_heart", qwen: "VeronicaDesk", want: "VeronicaDesk"},
		{name: "explicit default", legacy: "af_heart", qwen: "default", want: "default"},
		// Explicit choices must remain visible, even when validation rejects
		// them. Only the implicit cross-provider fallback is discarded.
		{name: "explicit unknown voice", legacy: "af_heart", qwen: "MyCustomVoice", want: "MyCustomVoice", invalid: true},
		{name: "external CLI default", legacy: "af_heart", external: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setConfigDir(t, t.TempDir())
			cfg := &config.Config{TTSProvider: "qwen3-tts", TTSVoice: tt.legacy, QwenTTSVoice: tt.qwen}
			if tt.external {
				cfg.QwenTTSBinary = "/custom/qwen-cli"
				cfg.QwenTTSModel = "/custom/model"
			}
			before := *cfg
			p, err := Create(cfg, "Qwen Buddy")
			if err != nil {
				t.Fatal(err)
			}
			p, err = Load(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if p.TTS.Provider != "qwen3-tts" || p.TTS.Voice != tt.want {
				t.Fatalf("persisted TTS = %+v, want qwen3-tts/%q", p.TTS, tt.want)
			}
			if cfg.TTSVoice != before.TTSVoice || cfg.QwenTTSVoice != before.QwenTTSVoice {
				t.Fatal("Create changed app-level voices")
			}
			Apply(cfg, p)
			if err := config.ValidateQwenTTSConfig(cfg); (err != nil) != tt.invalid {
				t.Fatalf("Qwen validation = %v, want invalid=%v", err, tt.invalid)
			}
			if cfg.TTSVoice != tt.legacy {
				t.Fatalf("Kokoro voice changed to %q", cfg.TTSVoice)
			}
		})
	}
}

func TestSeedQwenDefaultsDoesNotPinKokoroVoice(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cfg := &config.Config{TTSProvider: "qwen3-tts", TTSVoice: "af_heart"}
	if err := EnsureAndApply(cfg); err != nil {
		t.Fatal(err)
	}
	p, err := Load(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if p.TTS.Provider != "" || p.TTS.Voice != "" {
		t.Fatalf("default persona pins TTS: %+v", p.TTS)
	}
	if err := config.ValidateQwenTTSConfig(cfg); err != nil {
		t.Fatalf("fresh Qwen config cannot speak: %v", err)
	}
}
