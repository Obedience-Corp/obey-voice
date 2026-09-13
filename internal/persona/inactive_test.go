package persona

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func inactiveTestProfile(id string, inactive bool) *Profile {
	return &Profile{
		Schema: Schema, ID: id, DisplayName: id, Inactive: inactive,
		Brain:   Brain{Provider: "ollama", Model: "model-x"},
		TTS:     TTS{Provider: "kokoro", Voice: "af_heart", Tier: "1.7b"},
		Prompts: PromptRefs{Persona: id, Turn: "turn"},
	}
}

func TestSetInactivePersistsMarkerAndPreservesProfile(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "healthy"}
	p := inactiveTestProfile("sleepy", false)
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	got, err := SetInactive(cfg, "sleepy", true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Inactive || got.Brain.Model != p.Brain.Model || got.TTS != p.TTS || got.Prompts != p.Prompts {
		t.Fatalf("SetInactive changed profile fields: got %+v", got)
	}
	onDisk, err := Load("sleepy")
	if err != nil {
		t.Fatal(err)
	}
	if !onDisk.Inactive || onDisk.Brain != p.Brain || onDisk.TTS != p.TTS || onDisk.Prompts != p.Prompts {
		t.Fatalf("persisted profile changed fields: %+v", onDisk)
	}
	if _, err := SetInactive(cfg, "sleepy", false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := Load("sleepy"); err != nil || enabled.Inactive {
		t.Fatalf("re-enabled profile = %+v, err %v", enabled, err)
	}
}

func TestSetInactiveRefusesConfiguredActive(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(inactiveTestProfile("selected", false), false); err != nil {
		t.Fatal(err)
	}
	_, err := SetInactive(&config.Config{ActivePersona: "selected"}, "selected", true)
	if err == nil || !strings.Contains(err.Error(), "choose another default first") {
		t.Fatalf("SetInactive error = %v", err)
	}
	p, loadErr := Load("selected")
	if loadErr != nil || p.Inactive {
		t.Fatalf("profile was modified after refused disable: %+v, %v", p, loadErr)
	}
}

func TestInactiveRejectedByResolutionAndFallback(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(inactiveTestProfile("obey", true), false); err != nil {
		t.Fatal(err)
	}
	if err := Write(inactiveTestProfile("healthy", false), false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ActivePersona: "obey"}
	if _, err := ResolveBinding(cfg, "obey"); !errors.Is(err, ErrInactive) || !strings.Contains(err.Error(), "persona enable obey") {
		t.Fatalf("ResolveBinding inactive error = %v", err)
	}
	if err := EnsureAndApply(cfg); err != nil {
		t.Fatalf("EnsureAndApply: %v", err)
	}
	if cfg.ActivePersona == "obey" {
		t.Fatalf("EnsureAndApply selected inactive fallback: %q", cfg.ActivePersona)
	}
	if fallback, err := Load(cfg.ActivePersona); err != nil || fallback.Inactive {
		t.Fatalf("EnsureAndApply fallback = %q, profile=%+v, err %v", cfg.ActivePersona, fallback, err)
	}
}

func TestDeleteActiveRefusesWhenOnlyInactiveReplacement(t *testing.T) {
	setConfigDir(t, t.TempDir())
	active := inactiveTestProfile("active", false)
	other := inactiveTestProfile("other", true)
	if err := Write(active, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(other, false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ActivePersona: "active"}
	if _, err := Delete(cfg, "active"); err == nil || !strings.Contains(err.Error(), "no enabled replacement") {
		t.Fatalf("Delete error = %v", err)
	}
	if _, err := os.Stat(ProfilePath("active")); err != nil {
		t.Fatalf("active profile removed after refusal: %v", err)
	}
}
