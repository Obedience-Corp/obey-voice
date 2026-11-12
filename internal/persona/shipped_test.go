package persona

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// TestShippedCatalogIsSelfContained is the contract for adding a companion:
// drop shipped/<id>.yaml and prompts/defaults/persona.<id>.yaml. No Go
// special case. This fails if those two files disagree.
func TestShippedCatalogIsSelfContained(t *testing.T) {
	ids := ShippedIDs()
	if len(ids) == 0 || ids[0] != DefaultID {
		t.Fatalf("ShippedIDs() = %v, want %q first", ids, DefaultID)
	}
	seen := map[string]bool{}
	for _, p := range shippedProfiles() {
		if seen[p.ID] {
			t.Errorf("duplicate shipped id %q", p.ID)
		}
		seen[p.ID] = true
		if !p.Builtin {
			t.Errorf("%s: builtin = false", p.ID)
		}
		if p.Prompts.Persona != p.ID {
			t.Errorf("%s: prompts.persona = %q, want the profile id", p.ID, p.Prompts.Persona)
		}
		if _, err := (prompts.Resolver{}).Resolve(prompts.KindPersona, p.ID); err != nil {
			t.Errorf("%s: no embedded persona prompt named %q: %v", p.ID, p.ID, err)
		}
	}
	if !seen["samantha"] || !seen["veronica"] || !seen["jarvis"] || !seen["santa"] || !seen["dylan"] {
		t.Errorf("catalog = %v, want samantha, veronica, jarvis, santa, and dylan among companions", ids)
	}
}
