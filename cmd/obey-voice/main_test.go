package main

import (
	"os"
	"testing"
)

func TestAdoptLegacyEnv(t *testing.T) {
	t.Setenv("SAMANTHA_COLOR_PROFILE", "ansi")
	t.Setenv("SAMANTHA_OUTPUT_DEVICE", "legacy")
	t.Setenv("OBEY_VOICE_OUTPUT_DEVICE", "explicit")
	t.Setenv("OBEY_VOICE_COLOR_PROFILE", "")
	if err := os.Unsetenv("OBEY_VOICE_COLOR_PROFILE"); err != nil {
		t.Fatal(err)
	}

	adoptLegacyEnv()

	if got := os.Getenv("OBEY_VOICE_COLOR_PROFILE"); got != "ansi" {
		t.Fatalf("OBEY_VOICE_COLOR_PROFILE = %q, want legacy value adopted", got)
	}
	if got := os.Getenv("OBEY_VOICE_OUTPUT_DEVICE"); got != "explicit" {
		t.Fatalf("OBEY_VOICE_OUTPUT_DEVICE = %q, want explicit value to win over legacy", got)
	}
}
