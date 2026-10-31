package persona

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"gopkg.in/yaml.v3"
)

func TestExplicitNativeDefaultPreservesStandardSettingsAndGenericGuard(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("active_persona: old\nbrain_provider: ollama\nollama_model: standard-model\ntts_provider: kokoro\ntts_voice: af_heart\nvoice_worker_provider: claude\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Profile{Schema: Schema, ID: "native-default", DisplayName: "Native default", Prompts: PromptRefs{Persona: "samantha"}, Audio: &Audio{Mode: AudioNative, Controls: AudioControlsConversation, Transcript: AudioTranscriptOff}, TTS: TTS{Provider: "qwen3-tts", Voice: "saved-standard"}}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	previous := cfg.Clone()
	if err := Use(cfg, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatal("generic host lost activation guard", err)
	}
	if err := UseExperimentalNative(cfg, p.ID); err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != p.ID || cfg.BrainProvider != previous.BrainProvider || cfg.OllamaModel != previous.OllamaModel || cfg.TTSProvider != previous.TTSProvider || cfg.TTSVoice != previous.TTSVoice || cfg.VoiceWorkerProvider != previous.VoiceWorkerProvider {
		t.Fatal("native preference overwrote Standard settings")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := yaml.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["active_persona"] != p.ID || saved["tts_provider"] != "kokoro" || saved["tts_voice"] != "af_heart" || saved["ollama_model"] != "standard-model" || saved["voice_worker_provider"] != "claude" {
		t.Fatal("native default changed persisted Standard providers", saved)
	}
	p.Inactive = true
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	if err := UseExperimentalNative(cfg, p.ID); !errors.Is(err, ErrInactive) {
		t.Fatal("native bypassed inactive marker", err)
	}
}
