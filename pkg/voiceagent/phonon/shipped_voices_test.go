package phonon_test

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/phonon"
)

// A shipped Phonon persona must speak on a fresh native install, so managed
// setup has to provision and verify its pinned voice.
func TestShippedPhononVoicesAreProvisioned(t *testing.T) {
	for _, p := range persona.Builtins(nil) {
		if p.TTS.Provider != config.OpenAISpeechProviderName {
			continue
		}
		if !slices.Contains(phonon.NamedVoices, p.TTS.Voice) {
			t.Errorf("%s pins %q, which native Phonon setup does not provision (%v)", p.ID, p.TTS.Voice, phonon.NamedVoices)
		}
	}
}
