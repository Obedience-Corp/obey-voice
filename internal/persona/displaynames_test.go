package persona

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestEnsureAndApplyCollectsDisplayNames(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	writeConfigFile(t, dir, "agent_name: Custom\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) == 0 || len(cfg.PersonaDisplayNames) != len(profiles) {
		t.Fatalf("PersonaDisplayNames = %v for %d profiles", cfg.PersonaDisplayNames, len(profiles))
	}
	for i, p := range profiles {
		if cfg.PersonaDisplayNames[i] != p.DisplayName {
			t.Fatalf("name %d = %q, want %q", i, cfg.PersonaDisplayNames[i], p.DisplayName)
		}
	}
}

func TestBindingsFillDisplayNamesForRawConfig(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	writeConfigFile(t, dir, "agent_name: Custom\n")
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
	raw, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.PersonaDisplayNames) != 0 {
		t.Fatalf("LoadRaw should carry no names, got %v", raw.PersonaDisplayNames)
	}

	defaults, err := ResolveDefaultsBinding(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.Config().PersonaDisplayNames) == 0 {
		t.Fatal("defaults binding carries no persona names")
	}
	named, err := ResolveBinding(raw, DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if len(named.Config().PersonaDisplayNames) == 0 {
		t.Fatal("named binding carries no persona names")
	}
	if len(raw.PersonaDisplayNames) != 0 {
		t.Fatal("binding mutated the caller's config")
	}
}
