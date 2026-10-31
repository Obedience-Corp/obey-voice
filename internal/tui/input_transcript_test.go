package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
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

func TestLiveCaptionCrossesActualEventBridgeToOriginalRow(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	input := &utterance.Finalized{ID: "retained", CaptureEpoch: 17, Transcript: utterance.Transcript{State: utterance.Disabled}}
	m.seedTranscript([]brain.Turn{{Role: "user", Utterance: input}})
	bus := events.NewBus()
	bridge := newEventBridge(8)
	bridge.attach(bus)
	update := events.InputTranscriptUpdated{UtteranceID: input.ID, CaptureEpoch: input.CaptureEpoch, Transcript: utterance.Transcript{State: utterance.Ready, Text: "Explicit recording transcript."}}
	bus.Emit(update)
	select {
	case msg := <-bridge.ch:
		if !isDurableBridgeMsg(msg) {
			t.Fatal("caption is droppable")
		}
		m.handleEvent(msg.(busEventMsg).event)
	default:
		t.Fatal("actual event bridge dropped live caption update")
	}
	if m.plainTurns[0].text != update.Transcript.Text || !strings.Contains(m.View(), update.Transcript.Text) {
		t.Fatal("original row not updated", m.plainTurns)
	}
}
