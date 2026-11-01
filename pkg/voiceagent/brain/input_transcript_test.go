package brain

import (
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestAttributedCaptionSnapshotsAreDetachedAndDoNotCreateTurns(t *testing.T) {
	ref := utterance.AudioRef{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MIME: "audio/wav", SampleRate: 16000, Channels: 1, SampleCount: 1}
	input := &utterance.Finalized{ID: "captioned", CaptureEpoch: 7, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
	brain := &OllamaBrain{}
	brain.LoadHistory([]Turn{{Role: "user", Utterance: input}, {Role: "assistant", Content: "actual answer"}})
	transcript := utterance.Transcript{State: utterance.Ready, Text: "actual spoken request", Provider: "ollama", Model: "chosen-model"}
	if err := brain.UpdateInputTranscript("captioned", 8, transcript); err == nil {
		t.Fatal("caption crossed capture epoch")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := brain.UpdateInputTranscript("captioned", 7, transcript); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = brain.History()
			_ = brain.ContextHistory()
			_ = brain.ContinuitySnapshot()
		}
	}()
	wg.Wait()
	history := brain.History()
	if len(history) != 2 || history[0].Content != "actual spoken request" || history[1].Content != "actual answer" {
		t.Fatal(history)
	}
	history[0].Utterance.Transcript.Text = "mutated"
	if brain.History()[0].Utterance.Transcript.Text != "actual spoken request" {
		t.Fatal("caption snapshot aliases owner")
	}
	if brain.canonical[0].Utterance.Transcript.State != utterance.Pending {
		t.Fatal("caption mutated captured value")
	}
	if err := ValidateTextProjection(brain.ContextHistory()); err != nil {
		t.Fatal(err)
	}
	brain.ClearHistory()
	if err := brain.UpdateInputTranscript("captioned", 7, transcript); err == nil {
		t.Fatal("retired caption attached after clear")
	}
}
