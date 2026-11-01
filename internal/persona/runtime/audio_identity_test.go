package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestBindingHashIncludesNativePoliciesButNotInactiveStandardDrafts(t *testing.T) {
	catalog := persona.Catalog{Dir: t.TempDir()}
	profile := testProfile("audio-persona", "Audio", "obey")
	profile.Audio = &persona.Audio{Mode: persona.AudioNative, Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff, Model: "chosen", Endpoint: "http://localhost:18144"}
	base := &config.Config{OllamaHost: "http://localhost:11434"}
	selector := &Selector{opts: SelectionOptions{Pipeline: &pipeline.Pipeline{}}}
	snapshot := func() string {
		t.Helper()
		writeTestProfile(t, catalog.Dir, profile)
		binding, _, err := catalog.ResolveCandidateBindingWithIssues(base, profile.ID)
		if err != nil {
			t.Fatal(err)
		}
		return selector.snapshotFor(binding, "prompt", 1, "test").BindingHash
	}
	old := snapshot()
	profile.Audio.RetainAudio = true
	if next := snapshot(); next == old {
		t.Fatal("retention change did not invalidate native binding")
	} else {
		old = next
	}
	profile.Audio.Transcript = persona.AudioTranscriptAuto
	if next := snapshot(); next == old {
		t.Fatal("caption policy change did not invalidate native binding")
	}
	profile.Audio.Mode = persona.AudioStandard
	old = snapshot()
	profile.Audio.Model = "saved-inactive-model"
	profile.Audio.Transcript = persona.AudioTranscriptOff
	if snapshot() != old {
		t.Fatal("inactive Standard draft changed live resource identity")
	}
}
