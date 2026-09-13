package personaruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestInactivePersonaLeavesRosterAndReenableRestoresAmbiguity(t *testing.T) {
	hype := testProfile("veronica-hype", "Veronica Hype", "veronica-hype")
	hype.Aliases = []string{"Obey"}
	hype.Inactive = true
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("veronica", "Veronica", "veronica"), hype)
	defer f.close(t)
	ctx := context.Background()
	for input, want := range map[string]string{"Obey, hello": "obey", "Veronica, hello": "veronica"} {
		got, err := f.selector.Route(ctx, input, false)
		if err != nil || got.Envelope.PersonaID != want {
			t.Fatalf("Route(%q) = %+v, %v", input, got, err)
		}
	}
	before := f.selector.Snapshot()
	if _, err := f.selector.Select(ctx, hype.ID, "explicit"); !errors.Is(err, persona.ErrInactive) {
		t.Fatalf("inactive selection = %v", err)
	}
	if f.selector.Snapshot() != before {
		t.Fatal("inactive selection changed responder")
	}
	hype.Inactive = false
	writeTestProfile(t, f.catalog.Dir, hype)
	if _, err := f.selector.Route(ctx, "Veronica, hello", false); err == nil {
		t.Fatal("enabling conflicting persona must restore ambiguity, without full-name precedence")
	}
	if f.selector.Snapshot() != before {
		t.Fatal("ambiguous selection changed responder")
	}
}

func TestDisablingLivePersonaClosesAdmissionUntilEnabledSelection(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	p, err := f.catalog.Load("obey")
	if err != nil {
		t.Fatal(err)
	}
	p.Inactive = true
	writeTestProfile(t, f.catalog.Dir, *p)
	before := f.selector.Snapshot()
	for _, background := range []bool{false, true} {
		if _, err := f.selector.Route(context.Background(), "continue", background); !errors.Is(err, persona.ErrInactive) {
			t.Fatalf("admission after disable (background=%v) = %v", background, err)
		}
	}
	if f.selector.Snapshot() != before || f.pipeline.Brain != f.old {
		t.Fatal("inactive refusal changed existing identity/history")
	}
	if got, err := f.selector.Route(context.Background(), "Santa, hello", false); err != nil || got.Envelope.PersonaID != "santa" {
		t.Fatalf("enabled replacement = %+v, %v", got, err)
	}
}

func TestRestoredInactivePersonaCannotResumeUntilEnabled(t *testing.T) {
	f := newSelectorTestFixture(t)
	defer f.close(t)
	p, err := f.catalog.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Inactive = true
	writeTestProfile(t, f.catalog.Dir, *p)
	f.selector.RestoreSession(f.session.ID, p.ID, p.DisplayName, "old-prompt")
	if _, err := f.selector.Admit(context.Background()); err == nil {
		t.Fatal("restored inactive persona admitted response")
	}
	if _, err := f.selector.Select(context.Background(), p.ID, "explicit"); !errors.Is(err, persona.ErrInactive) {
		t.Fatalf("restored inactive selection = %v", err)
	}
	p.Inactive = false
	writeTestProfile(t, f.catalog.Dir, *p)
	if _, err := f.selector.Select(context.Background(), p.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
}
