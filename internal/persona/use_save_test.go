package persona

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestUseSavesConfigOnce(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	profile := &Profile{
		Schema: Schema, ID: "eight", DisplayName: "Eight",
		TTS:     TTS{Provider: config.Audio8ProviderName, Voice: "my-registered-voice"},
		Prompts: PromptRefs{Persona: "eight"},
	}
	if err := Write(profile, true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("agent_name: Other\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{TTSProvider: "kokoro"}
	if err := Use(cfg, "eight"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("backups after one switch = %v, want exactly one", backups)
	}
	for key, want := range map[string]string{
		"active_persona": "eight", "agent_name": "Eight", "persona": "eight",
		"tts_provider": config.Audio8ProviderName, "audio8_voice": "my-registered-voice",
	} {
		if got, _ := config.Get(key).(string); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if err := Use(cfg, "eight"); err != nil {
		t.Fatalf("second Use: %v", err)
	}
	if again, _ := filepath.Glob(path + ".bak.*"); len(again) != 1 {
		t.Errorf("switching to the already-active persona made a backup: %v", again)
	}
}
