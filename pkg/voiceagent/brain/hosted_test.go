package brain

import "testing"

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
