package persona

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Creating a persona with an audio8 voice used to fail with
//
//	saving persona tts: tts_voice expects one of af_alloy, ...
//
// because this routing table had no audio8 case: the voice fell through to
// tts_voice, whose schema enum is Kokoro's voice list. audio8 gets its own key,
// like qwen3-tts, and these are the four places that have to agree about it.
func TestApplyRoutesAnAudio8Voice(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		profile     TTS
		wantAudio8  string
		wantKokoro  string
		wantQwen    string
		wantVoiceOf string
	}{
		{
			name:        "the packaged reference reaches audio8_voice, not tts_voice",
			cfg:         config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"},
			profile:     TTS{Provider: config.Audio8ProviderName, Voice: config.Audio8PackagedVoice},
			wantAudio8:  config.Audio8PackagedVoice,
			wantKokoro:  "af_heart",
			wantVoiceOf: config.Audio8PackagedVoice,
		},
		{
			name:        "a registered voice reaches audio8_voice",
			cfg:         config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"},
			profile:     TTS{Provider: config.Audio8ProviderName, Voice: "my-voice"},
			wantAudio8:  "my-voice",
			wantKokoro:  "af_heart",
			wantVoiceOf: "my-voice",
		},
		{
			// The reverse hazard the qwen precedent implies: coming back to
			// kokoro must write the Kokoro key, and must not let the stale
			// audio8 name decide what is spoken.
			name:        "switching back to kokoro writes tts_voice",
			cfg:         config.Config{TTSProvider: config.Audio8ProviderName, Audio8Voice: "my-voice"},
			profile:     TTS{Provider: "kokoro", Voice: "af_sky"},
			wantAudio8:  "my-voice",
			wantKokoro:  "af_sky",
			wantVoiceOf: "af_sky",
		},
		{
			name:        "an audio8 persona with no voice leaves the key alone",
			cfg:         config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"},
			profile:     TTS{Provider: config.Audio8ProviderName},
			wantKokoro:  "af_heart",
			wantVoiceOf: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			Apply(&cfg, &Profile{ID: "p", DisplayName: "P", Prompts: PromptRefs{Persona: "p"}, TTS: tt.profile})
			if cfg.Audio8Voice != tt.wantAudio8 {
				t.Errorf("Audio8Voice = %q, want %q", cfg.Audio8Voice, tt.wantAudio8)
			}
			if cfg.TTSVoice != tt.wantKokoro {
				t.Errorf("TTSVoice = %q, want %q", cfg.TTSVoice, tt.wantKokoro)
			}
			if cfg.QwenTTSVoice != tt.wantQwen {
				t.Errorf("QwenTTSVoice = %q, want %q", cfg.QwenTTSVoice, tt.wantQwen)
			}
			// The profile a surface reads back must be the one it wrote.
			if got := voiceForProvider(&cfg, cfg.TTSProvider); got != tt.wantVoiceOf {
				t.Errorf("voiceForProvider(%q) = %q, want %q", cfg.TTSProvider, got, tt.wantVoiceOf)
			}
		})
	}
}

// TestUseAnAudio8PersonaSaves is the bug as reported: switching to a persona
// whose TTS stack is audio8 failed at the write, because PersistTTS pushed the
// voice into tts_voice and `config set` refused it against the Kokoro enum.
func TestUseAnAudio8PersonaSaves(t *testing.T) {
	for _, voice := range []string{config.Audio8PackagedVoice, "my-registered-voice"} {
		t.Run(voice, func(t *testing.T) {
			setConfigDir(t, t.TempDir())
			profile := &Profile{
				Schema: Schema, ID: "eight", DisplayName: "Eight",
				TTS:     TTS{Provider: config.Audio8ProviderName, Voice: voice},
				Prompts: PromptRefs{Persona: "eight"},
			}
			if err := Write(profile, true); err != nil {
				t.Fatalf("Write: %v", err)
			}
			cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}
			if err := Use(cfg, "eight"); err != nil {
				t.Fatalf("Use: %v", err)
			}
			if cfg.TTSProvider != config.Audio8ProviderName || cfg.Audio8Voice != voice {
				t.Fatalf("after Use: provider %q voice %q, want audio8/%s", cfg.TTSProvider, cfg.Audio8Voice, voice)
			}
			// And it must be on disk, not only on the in-memory copy.
			if got, _ := config.Get("audio8_voice").(string); got != voice {
				t.Errorf("audio8_voice persisted as %q, want %q", got, voice)
			}
			if got, _ := config.Get("tts_voice").(string); got == voice {
				t.Errorf("the audio8 voice was written into tts_voice, which only Kokoro reads")
			}
		})
	}
}

// FromConfig is the read half of the round trip: a config speaking audio8 must
// describe itself with the audio8 voice, not with whatever tts_voice holds.
func TestFromConfigCapturesTheAudio8Voice(t *testing.T) {
	p := FromConfig(&config.Config{
		AgentName:   "A",
		Persona:     "samantha",
		TTSProvider: config.Audio8ProviderName,
		Audio8Voice: "my-voice",
		TTSVoice:    "af_heart",
	})
	if p.TTS.Provider != config.Audio8ProviderName || p.TTS.Voice != "my-voice" {
		t.Fatalf("FromConfig TTS = %+v, want audio8/my-voice", p.TTS)
	}
}

// A persona that selects audio8 must show audio8_voice as an overridden key, or
// `config get` would report the global value as if the persona did not decide
// it.
func TestOverriddenKeysReportsTheAudio8Voice(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}
	profile := &Profile{
		ID: "p", DisplayName: "P", Prompts: PromptRefs{Persona: "p"},
		TTS: TTS{Provider: config.Audio8ProviderName, Voice: "my-voice"},
	}
	got := strings.Join(OverriddenKeys(cfg, profile), ",")
	if !strings.Contains(got, "audio8_voice") {
		t.Errorf("OverriddenKeys = %q, want it to include audio8_voice", got)
	}
	if strings.Contains(got, "tts_voice") {
		t.Errorf("OverriddenKeys = %q; an audio8 persona does not decide the Kokoro voice", got)
	}
}
