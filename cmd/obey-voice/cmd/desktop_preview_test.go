//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestDesktopSavedPersonaPreviewSelection(t *testing.T) {
	dir := personaEnv(t)
	before, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := persona.TTS{Provider: "qwen3-tts", Voice: "Ryan", Tier: "1.7b"}
	writePersona(t, &persona.Profile{Schema: persona.Schema, ID: "sample", DisplayName: "Sample", TTS: want, Inactive: true, Prompts: persona.PromptRefs{Persona: "sample"}})
	got, err := desktopPreviewSelection(DesktopVoiceSelection{PersonaID: "sample"})
	if err != nil || got != want {
		t.Fatalf("saved selection=%+v err=%v", got, err)
	}
	got, err = desktopPreviewSelection(DesktopVoiceSelection{Provider: "kokoro", Voice: "am_santa"})
	if err != nil || got.Voice != "am_santa" {
		t.Fatalf("draft selection=%+v err=%v", got, err)
	}
	for _, selection := range []DesktopVoiceSelection{{PersonaID: "missing"}, {PersonaID: "../sample"}, {PersonaID: "sample", Voice: "override"}} {
		if _, err := desktopPreviewSelection(selection); err == nil {
			t.Fatalf("accepted invalid selection: %+v", selection)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preview selection changed active config")
	}
}
