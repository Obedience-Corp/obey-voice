package brain

import "testing"

// The host records the shared transcript for exactly the providers that keep
// session history without speaker identity.
func TestKeepsSharedHistoryClassifiesProviders(t *testing.T) {
	for name, p := range map[string]Provider{"ollama": (*OllamaBrain)(nil), "codex": (*CodexBrain)(nil), "muse-spark": (*MuseSparkBrain)(nil), "obey": (*ObeyBrain)(nil)} {
		if !KeepsSharedHistory(p) {
			t.Errorf("%s should keep the shared history itself", name)
		}
	}
	for name, p := range map[string]Provider{"claude": (*Brain)(nil), "grok": (*GrokBrain)(nil), "cursor": (*CursorBrain)(nil), "none": nil} {
		if KeepsSharedHistory(p) {
			t.Errorf("%s should be projected from the host transcript", name)
		}
	}
}
