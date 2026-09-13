package personaruntime

import (
	"context"
	"strings"
	"testing"
)

func TestSelectorUnrelatedNameCollisionDoesNotBlockConversation(t *testing.T) {
	f := newSelectorTestFixture(t,
		testProfile("obey", "Obey", "obey"),
		testProfile("veronica", "Veronica", "veronica"),
		testProfile("veronica-hype", "Veronica Hype", "veronica-hype"),
	)
	defer f.close(t)

	// These profiles reproduce the operator's installed catalog. Every input
	// used to fail while constructing the roster, before addressing was checked.
	for _, text := range []string{"tell me a joke", "Obey, tell me a joke", "continue", "Veronica is a persona"} {
		admitted, err := f.selector.Route(context.Background(), text, false)
		if err != nil || admitted.Envelope.PersonaID != "obey" {
			t.Fatalf("Route(%q) = %+v, %v; want the current responder", text, admitted, err)
		}
	}
	before := f.selector.Snapshot()
	for _, text := range []string{"Veronica", "Veronica, tell me a joke", "switch to Veronica"} {
		_, err := f.selector.Route(context.Background(), text, false)
		if err == nil || !strings.Contains(err.Error(), "veronica-hype") || !strings.Contains(err.Error(), "veronica") {
			t.Fatalf("ambiguous Route(%q) = %v; want actionable refusal", text, err)
		}
		if f.selector.Snapshot() != before {
			t.Fatal("ambiguous address changed selection")
		}
	}
	admitted, err := f.selector.Route(context.Background(), "Veronica Hype, tell me a joke", false)
	if err != nil || admitted.Envelope.PersonaID != "veronica-hype" {
		t.Fatalf("unique full name = %+v, %v", admitted, err)
	}
	// The persona picker selects an ID, so it remains a way to disambiguate
	// even when this persona's full display name is also somebody else's alias.
	if _, err := f.selector.Select(context.Background(), "veronica", "explicit"); err != nil {
		t.Fatalf("explicit ID selection: %v", err)
	}
	admitted, err = f.selector.Route(context.Background(), "continue", false)
	if err != nil || admitted.Envelope.PersonaID != "veronica" {
		t.Fatalf("follow-up after explicit ID selection = %+v, %v", admitted, err)
	}
}
