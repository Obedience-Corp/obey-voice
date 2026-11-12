package brain

import (
	"strings"
	"testing"
)

func TestEveryRegisteredProviderIsClassified(t *testing.T) {
	for _, spec := range Providers() {
		if _, ok := hostedProviders[spec.Name]; !ok {
			t.Errorf("provider %q is registered but not classified as hosted or local", spec.Name)
		}
	}
	for name := range hostedProviders {
		found := false
		for _, spec := range Providers() {
			found = found || spec.Name == name
		}
		if !found {
			t.Errorf("classification names %q, which is not a registered provider", name)
		}
	}
}

func TestIsHosted(t *testing.T) {
	for provider, want := range map[string]bool{
		"ollama": false, "": false, " Ollama ": false,
		"claude": true, "Claude": true, "codex": true, "cursor": true, "grok": true, "muse-spark": true, "obey": true,
		"a-provider-added-later": true,
	} {
		if got := IsHosted(provider); got != want {
			t.Errorf("IsHosted(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestHostedSharedHistorySendsOnlyItsWorkingView(t *testing.T) {
	canonical := []Turn{
		{Role: "user", Content: "A private line said before the invitation."},
		{Role: "assistant", Content: "Ember noted it.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	codex, _ := codexFixture(t)
	for name, b := range map[string]interface {
		ContinuityTransferProvider
		buildPrompt(string, StreamOptions) string
	}{"codex": codex, "muse-spark": testMuse(&fakeMuse{}), "obey": obeyTestBrain(nil)} {
		t.Run(name, func(t *testing.T) {
			if !HostedSharedHistory(b.(Provider)) || !IsHosted(name) {
				t.Fatalf("%s keeps the shared history on a hosted service but is not classified so", name)
			}
			if err := b.ImportContinuity(ContinuitySnapshot{Canonical: canonical, Working: []Turn{}}); err != nil {
				t.Fatal(err)
			}
			prompt := b.buildPrompt("Hello.", StreamOptions{})
			if strings.Contains(prompt, "private line") || strings.Contains(prompt, "Ember") {
				t.Fatalf("the prompt carries turns outside the working view:\n%s", prompt)
			}
			if got := b.ContinuitySnapshot(); len(got.Canonical) != len(canonical) || got.Working == nil || len(got.Working) != 0 {
				t.Fatalf("snapshot = %+v, want the whole record and an empty view", got)
			}
		})
	}
	for _, p := range []Provider{&OllamaBrain{}, &Brain{}, &GrokBrain{}, &CursorBrain{}} {
		if HostedSharedHistory(p) {
			t.Errorf("%T does not keep the shared history on a hosted service", p)
		}
	}
}
