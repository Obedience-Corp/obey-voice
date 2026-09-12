package personaruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func writeMalformedPersona(t *testing.T, root, id string) {
	t.Helper()
	path := filepath.Join(root, id, "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelectorRoutesHealthyProfileAndWarnsForMalformedUnrelatedProfile(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	bus := events.NewBus()
	f.pipeline.Events = bus
	var warnings []events.Error
	events.Subscribe(bus, func(e events.Error) {
		if e.Stage == "persona_roster" {
			warnings = append(warnings, e)
		}
	})
	writeMalformedPersona(t, f.catalog.Dir, "broken")

	admitted, err := f.selector.Route(context.Background(), "tell me a joke", false)
	if err != nil {
		t.Fatalf("healthy nameless route failed: %v", err)
	}
	if admitted.Envelope.PersonaID != "samantha" || admitted.Request != "tell me a joke" {
		t.Fatalf("healthy route = %+v", admitted)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0].Message, "broken") {
		t.Fatalf("roster warnings = %+v, want malformed broken profile warning", warnings)
	}
}

func TestSelectorKeepsLastGoodBindingAfterMalformedCurrentEdit(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
	defer f.close(t)
	bus := events.NewBus()
	f.pipeline.Events = bus
	writeMalformedPersona(t, f.catalog.Dir, "samantha")
	before := f.selector.Snapshot()

	admitted, err := f.selector.Route(context.Background(), "continue the conversation", false)
	if err != nil {
		t.Fatalf("last-good route failed after malformed edit: %v", err)
	}
	if admitted.Envelope.PersonaID != before.PersonaID || f.selector.Snapshot() != before {
		t.Fatalf("binding changed after malformed edit: before=%+v after=%+v", before, f.selector.Snapshot())
	}
	if f.pipeline.Brain != f.old {
		t.Fatal("malformed current edit replaced the live brain")
	}
}

func TestSelectorKeepsLastGoodBindingAfterMalformedPromptEdit(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
	defer f.close(t)
	promptPath := filepath.Join(f.base.PromptsDir, "persona", "samantha.yaml")
	if err := os.WriteFile(promptPath, []byte("prompt: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := f.selector.Snapshot()

	admitted, err := f.selector.Route(context.Background(), "continue the conversation", false)
	if err != nil {
		t.Fatalf("last-good route failed after malformed prompt edit: %v", err)
	}
	if admitted.Envelope.PersonaID != before.PersonaID || f.selector.Snapshot() != before {
		t.Fatalf("binding changed after malformed prompt: before=%+v after=%+v", before, f.selector.Snapshot())
	}
}

func TestSelectorExplicitMalformedTargetFailsWithoutFallback(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
	defer f.close(t)
	writeMalformedPersona(t, f.catalog.Dir, "santa")
	before := f.selector.Snapshot()

	_, err := f.selector.Route(context.Background(), "Santa, tell me a joke", false)
	if err == nil || !strings.Contains(err.Error(), "resolve persona \"santa\"") || !strings.Contains(err.Error(), "unavailable until repaired") {
		t.Fatalf("malformed explicit target error = %v, want hard failure", err)
	}
	if got := f.selector.Snapshot(); got != before || f.pipeline.Brain != f.old {
		t.Fatalf("explicit malformed target changed live binding: before=%+v after=%+v", before, got)
	}
}

func TestSelectorRejectsMalformedIDCollidingWithHealthyAlias(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
	defer f.close(t)
	f.selector.opts.Base.NameSummonAliases = []string{"Santa"}
	writeMalformedPersona(t, f.catalog.Dir, "santa")

	_, err := f.selector.Route(context.Background(), "Santa, tell me a joke", false)
	if err == nil || !strings.Contains(err.Error(), "santa") || !strings.Contains(err.Error(), "samantha") {
		t.Fatalf("collision route error = %v, want healthy/broken name collision", err)
	}
	if f.selector.Snapshot().PersonaID != "samantha" || f.pipeline.Brain != f.old {
		t.Fatal("roster collision changed the live binding")
	}
}

func TestSelectorRestoredMissingPersonaRemainsClosed(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
	defer f.close(t)
	f.selector.RestoreSession(f.session.ID, "deleted", "Deleted", "old-prompt")

	if _, err := f.selector.Route(context.Background(), "continue the conversation", false); err == nil {
		t.Fatal("restored missing persona admitted a turn")
	} else if !strings.Contains(err.Error(), "requires a valid persona selection") {
		t.Fatalf("restored missing persona error = %v", err)
	}
	if _, err := f.selector.Select(context.Background(), "deleted", "explicit"); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit deleted selection error = %v, want missing-profile failure", err)
	}
}

func TestCatalogListSkipsMalformedEntry(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	writeMalformedPersona(t, f.catalog.Dir, "broken")
	profiles, issues, err := f.catalog.ListWithIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || len(issues) != 1 || issues[0].ID != "broken" {
		t.Fatalf("profiles/issues = %v/%+v", profiles, issues)
	}
	if _, err := f.catalog.ResolveBinding(f.base, "broken"); err == nil {
		t.Fatal("explicit malformed target resolved")
	}
}

func TestSelectorUseDefaultsKeepsMalformedLiveProfileForForegroundAndBackground(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile(persona.DefaultID, persona.DefaultDisplayName, persona.DefaultID))
	defer f.close(t)
	config.SetConfigDirForTest(t, filepath.Dir(f.catalog.Dir))
	f.selector.opts.UseDefaults = true
	before := f.selector.Snapshot()
	writeMalformedPersona(t, f.catalog.Dir, persona.DefaultID)
	for _, background := range []bool{false, true} {
		admitted, err := f.selector.Route(context.Background(), "continue the conversation", background)
		if err != nil {
			t.Fatalf("defaults admission background=%v: %v", background, err)
		}
		if admitted.Envelope.PersonaID != before.PersonaID || f.selector.Snapshot() != before || f.pipeline.Brain != f.old {
			t.Fatalf("defaults binding changed for background=%v: %+v", background, admitted)
		}
	}
	if _, err := f.selector.Select(context.Background(), persona.DefaultID, "explicit"); err == nil {
		t.Fatal("explicit malformed default selection succeeded")
	}
}

func TestMalformedProfileRespectsWorkDisabledConversationBoundary(t *testing.T) {
	for _, state := range []string{"live", "cleared", "restored", "closed", "deleted"} {
		t.Run(state, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"))
			defer f.close(t)
			if err := f.pipeline.BeginConversationIdentity(f.session.ID); err != nil {
				t.Fatal(err)
			}
			var err error
			f.selector, err = NewSelector(f.selector.opts)
			if err != nil {
				t.Fatal(err)
			}
			before := f.selector.Snapshot()
			if before.ConversationEpoch == "" || f.pipeline.Agency != nil {
				t.Fatal("fixture must have a normal conversation identity without background work")
			}
			writeMalformedPersona(t, f.catalog.Dir, "samantha")
			switch state {
			case "cleared":
				err = f.pipeline.ClearConversation()
			case "restored":
				f.selector.RestoreSession(f.session.ID, "samantha", "Samantha", before.PromptHash)
			case "closed":
				err = f.pipeline.CloseWork()
			case "deleted":
				err = os.Remove(filepath.Join(f.catalog.Dir, "samantha", "persona.yaml"))
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, background := range []bool{false, true} {
				admitted, err := f.selector.Route(context.Background(), "continue the conversation", background)
				if state == "live" {
					if err != nil || admitted.Envelope.ConversationEpoch != before.ConversationEpoch || f.selector.Snapshot() != before {
						t.Fatalf("live last-good admission background=%v: %+v, %v", background, admitted, err)
					}
				} else if err == nil {
					t.Fatalf("%s identity admitted background=%v with a broken profile", state, background)
				}
			}
		})
	}
}
