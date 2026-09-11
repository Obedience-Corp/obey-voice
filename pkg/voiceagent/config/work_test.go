package config

import (
	"testing"
)

func TestExperimentalWorkRequiresExplicitOptIn(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	resetViper(t)
	t.Setenv("VOICE_WORK_ENABLED", "")
	cfg, err := LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoiceWorkEnabled {
		t.Fatal("experimental work enabled by default")
	}
	t.Setenv("VOICE_WORK_ENABLED", "true")
	cfg, err = LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VoiceWorkEnabled {
		t.Fatal("explicit work opt-in ignored")
	}
	t.Setenv("VOICE_WORK_ENABLED", "false")
	cfg, err = LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoiceWorkEnabled {
		t.Fatal("work opt-out ignored")
	}
}
