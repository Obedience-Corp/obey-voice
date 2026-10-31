package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestAudioOnlyInputCaptionUpdatesOriginalRowAndRejectsWrongEpoch(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	input := &utterance.Finalized{ID: "audio-row", CaptureEpoch: 9, Transcript: utterance.Transcript{State: utterance.Pending}}
	m.handleEvent(events.UserInput{Utterance: input})
	if len(m.plainTurns) != 1 || !strings.Contains(m.plainTurns[0].text, "transcript pending") {
		t.Fatal(m.plainTurns)
	}
	update := events.InputTranscriptUpdated{UtteranceID: input.ID, CaptureEpoch: 10, Transcript: utterance.Transcript{State: utterance.Ready, Text: "wrong"}}
	m.handleEvent(update)
	if strings.Contains(m.plainTurns[0].text, "wrong") {
		t.Fatal("caption crossed epoch")
	}
	update.CaptureEpoch = 9
	update.Transcript.Text = "actual spoken request"
	m.handleEvent(update)
	if len(m.plainTurns) != 1 || m.plainTurns[0].text != "actual spoken request" {
		t.Fatal(m.plainTurns)
	}
	if !strings.Contains(m.View(), "actual spoken request") {
		t.Fatal("caption missing from real view")
	}
	if input.Transcript.State != utterance.Pending {
		t.Fatal("UI mutated source event")
	}
}
