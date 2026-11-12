package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func plainTexts(m conversationModel) []string {
	out := make([]string, 0, len(m.plainTurns))
	for _, turn := range m.plainTurns {
		out = append(out, turn.text)
	}
	return out
}

// A result queued behind another persona's reply joins the thread when its
// speech starts, after that reply, not when the job finishes.
func TestSpokenWorkerReplyJoinsThreadWhenItsSpeechStarts(t *testing.T) {
	m := sizedConversation(t, 100, 28)
	dylan := &events.ResponseEnvelope{PersonaID: "dylan", DisplayName: "Dylan", ResponseID: "work-1"}
	m.handleEvent(events.ResponseDelta{Text: "Raw is answering"})
	m.handleEvent(events.JobFinished{ID: "job", Summary: "The fixture has three files.", Envelope: dylan, SpeechPending: true})
	if len(m.plainTurns) != 0 || strings.Contains(stripANSI(m.View()), "three files") {
		t.Fatalf("result shown before its speech: %v", plainTexts(m))
	}
	if _, running := m.jobs["job"]; running {
		t.Fatal("finished job still counted on the chip")
	}
	m.handleEvent(events.ResponseReady{Response: "Raw's answer.", Envelope: &events.ResponseEnvelope{PersonaID: "raw", DisplayName: "Raw"}})
	delivered := events.JobResultDelivered{ID: "job", Summary: "The fixture has three files.", Envelope: dylan, Spoken: true}
	m.handleEvent(delivered)
	m.handleEvent(delivered)
	if got := plainTexts(m); len(got) != 2 || got[0] != "Raw's answer." || got[1] != "The fixture has three files." {
		t.Fatalf("thread = %v, want the reply then the result", got)
	}
	view := stripANSI(m.View())
	if strings.Index(view, "Raw's answer.") > strings.Index(view, "three files") || !strings.Contains(view, "Dylan") {
		t.Fatalf("result misplaced or misattributed:\n%s", view)
	}
}

// Delivery can overtake the finish event; the reply still appears once.
func TestWorkerReplyDeliveredBeforeItsFinishShowsOnce(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.handleEvent(events.JobQueued{ID: "job", Task: "look it up"})
	m.handleEvent(events.JobResultDelivered{ID: "job", Summary: "Found it."})
	m.handleEvent(events.JobFinished{ID: "job", Summary: "Found it.", SpeechPending: true})
	if got := plainTexts(m); len(got) != 1 || got[0] != "Found it." {
		t.Fatalf("thread = %v", got)
	}
	if len(m.jobs) != 0 {
		t.Fatalf("finished job still tracked: %+v", m.jobs)
	}
}

func TestDeliveredWorkerReplyFromRetiredScopeStaysOut(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	current := work.WorkScope{SessionID: "same", RuntimeEpoch: "runtime", ConversationEpoch: "new"}
	m.deps.workScope = func() work.WorkScope { return current }
	old := current
	old.ConversationEpoch = "old"
	m.handleEvent(events.JobResultDelivered{ID: "old", Summary: "Stale worker result", Scope: old})
	if len(m.plainTurns) != 0 {
		t.Fatal("stale result entered the new thread")
	}
}

func TestBridgeKeepsWorkerReplyDeliveryUnderFlood(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(8)
	bridge.attach(bus)
	bus.Emit(events.JobResultDelivered{ID: "job", Summary: "Found it."})
	for i := range 40 {
		bus.Emit(events.TranscriptPartial{Text: fmt.Sprintf("partial-%d", i)})
		bus.Emit(events.SpeakingStarted{Text: fmt.Sprintf("seg-%d", i)})
	}
	for {
		select {
		case msg := <-bridge.ch:
			if e, ok := msg.(busEventMsg).event.(events.JobResultDelivered); ok && e.ID == "job" {
				return
			}
			continue
		default:
		}
		t.Fatal("worker reply delivery dropped under flood")
	}
}
