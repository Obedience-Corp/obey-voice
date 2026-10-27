package persona

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestFreshInstallSantaCanBeSelected(t *testing.T) {
	newInstall(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Use(cfg, "santa"); err != nil {
		t.Fatal(err)
	}
	if cfg.ActivePersona != "santa" || cfg.AgentName != "Santa" || cfg.TTSProvider != "kokoro" || cfg.TTSVoice != "am_santa" {
		t.Fatalf("Santa selection = %s/%s, speech %s/%s", cfg.ActivePersona, cfg.AgentName, cfg.TTSProvider, cfg.TTSVoice)
	}
	text, err := LoadSystemPrompt("santa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Santa Claus") || !strings.Contains(text, "{agent_name}") {
		t.Fatalf("Santa's prompt did not resolve: %s", text)
	}
	// Selection is persisted, not just a mutation of the caller's config.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ActivePersona != "santa" || reloaded.TTSVoice != "am_santa" {
		t.Fatalf("reload lost Santa: %s/%s", reloaded.ActivePersona, reloaded.TTSVoice)
	}
}

func TestCatalogUpgradePreservesOccupiedProfiles(t *testing.T) {
	for _, tt := range []struct {
		id, body string
	}{
		{"jarvis", "builtin: true\ntts:\n  provider: kokoro\n  voice: bm_george\n"},
		{"santa", "builtin: false\ntts:\n  provider: kokoro\n  voice: am_michael\n"},
		{"obey", "builtin: true\naliases: [My Helper]\ntts: {}\n"},
	} {
		t.Run(tt.id, func(t *testing.T) {
			dir := newInstall(t)
			writeConfigFile(t, dir, "active_persona: "+tt.id+"\n")
			body := "schema: " + Schema + "\nid: " + tt.id + "\ndisplay_name: My Companion\nprompts:\n  persona: " + tt.id + "\n" + tt.body
			path := writeProfileFile(t, dir, tt.id, body)
			for range 2 {
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ActivePersona != tt.id || cfg.AgentName != "My Companion" {
					t.Fatalf("upgrade changed selected identity: %s/%s", cfg.ActivePersona, cfg.AgentName)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != body {
					t.Fatalf("occupied profile was rewritten: %s, %v", got, err)
				}
				for _, id := range ShippedIDs() {
					if _, err := Load(id); err != nil {
						t.Fatalf("missing companion %s: %v", id, err)
					}
				}
			}
		})
	}
}

func TestSeededObeyAliasesRouteWithoutFuzzyMatching(t *testing.T) {
	newInstall(t)
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
	profiles, err := List()
	if err != nil {
		t.Fatal(err)
	}
	var identities []summon.Identity
	for _, p := range profiles {
		identities = append(identities, summon.Identity{ID: p.ID, DisplayName: p.DisplayName, Aliases: p.Aliases})
	}
	roster, err := summon.NewRoster(identities)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"Obey", "Obi", "Obie.", "Hey Oh bay", "switch to Obi"} {
		got := roster.ResolveExactControl(input)
		if got.Kind != summon.ExactControlValid || got.Address.PersonaID != DefaultID {
			t.Errorf("exact address %q = %+v", input, got)
		}
	}
	for _, input := range []string{"okay", "oh baby", "obituary", "Obi is a nickname", "Obi and Santa", `"Obi"`} {
		if got := roster.ResolveExactControl(input); got.Kind == summon.ExactControlValid {
			t.Errorf("ordinary or multi-target input %q became an exact selection: %+v", input, got)
		}
	}
	// A user-created persona owning the nickname must cause a clear refusal,
	// not silently lose its identity to a built-in alias.
	identities = append(identities, summon.Identity{ID: "my-obi", DisplayName: "Obi"})
	roster, err = summon.NewRoster(identities)
	if err != nil {
		t.Fatal(err)
	}
	got := roster.ResolveExactControl("Obi")
	var ambiguous *summon.AmbiguousTargetError
	if got.Kind != summon.ExactControlInvalid || !errors.As(got.Err, &ambiguous) {
		t.Fatalf("shared nickname was not refused: %+v", got)
	}
}

func TestShippedAliasesAreIndependentCopies(t *testing.T) {
	first := Builtins(nil)
	if len(first[0].Aliases) == 0 {
		t.Fatal("missing Obey aliases")
	}
	original := first[0].Aliases[0]
	first[0].Aliases[0] = "customized"
	if got := Builtins(nil)[0].Aliases[0]; got != original {
		t.Fatalf("editing a returned profile mutated the embedded catalog: %q", got)
	}
}
