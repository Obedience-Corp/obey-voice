package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestPersonaDisableEnablePersistsAndUsesEnvelope(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "disable", "uncle-fu", "--json")
	if err != nil {
		t.Fatalf("disable: %v (%s)", err, out)
	}
	var disabled personaResultJSON
	if err := json.Unmarshal([]byte(out), &disabled); err != nil {
		t.Fatalf("decode disable: %v", err)
	}
	if disabled.Persona == nil || !disabled.Persona.Inactive || len(disabled.Changed) != 1 || disabled.Changed[0] != "inactive" {
		t.Fatalf("disable result = %+v", disabled)
	}
	if p, err := persona.Load("uncle-fu"); err != nil || !p.Inactive {
		t.Fatalf("saved profile = %+v, err=%v", p, err)
	}

	out, err = runPersona(t, "enable", "uncle-fu", "--json")
	if err != nil {
		t.Fatalf("enable: %v (%s)", err, out)
	}
	var enabled personaResultJSON
	if err := json.Unmarshal([]byte(out), &enabled); err != nil {
		t.Fatalf("decode enable: %v", err)
	}
	if enabled.Persona == nil || enabled.Persona.Inactive {
		t.Fatalf("enable result = %+v", enabled)
	}
}

func TestPersonaListMarksInactive(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	if _, err := setPersonaInactive("uncle-fu", true); err != nil {
		t.Fatal(err)
	}
	out, err := runPersona(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inactive") {
		t.Fatalf("list = %q, want inactive indicator", out)
	}
}

func TestPersonaManagementRecoversManuallyDisabledDefault(t *testing.T) {
	personaEnv(t)
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Inactive = true
	writePersona(t, p)
	for _, args := range [][]string{
		{"list", "--json"},
		{"show", "samantha", "--json"},
		{"edit", "samantha", "--display-name", "Samantha", "--json"},
		{"enable", "samantha", "--json"},
		{"use", "samantha", "--json"},
	} {
		if out, err := runPersona(t, args...); err != nil {
			t.Fatalf("%v: %v (%s)", args, err, out)
		}
	}
	p, err = persona.Load("samantha")
	if err != nil || p.Inactive {
		t.Fatalf("recovered profile = %+v, %v", p, err)
	}
}
