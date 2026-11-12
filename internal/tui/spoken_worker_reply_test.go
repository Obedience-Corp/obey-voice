package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
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

// The terminal renderer never draws a queued result before the reply that was
// spoken ahead of it.
func TestSpokenWorkerReplyTerminalOrder(t *testing.T) {
	dylan := &events.ResponseEnvelope{PersonaID: "dylan", DisplayName: "Dylan", ResponseID: "work-1"}
	h := &spokenReplyTerminal{model: sizedConversation(t, 100, 26), steps: []events.Event{
		events.ResponseDelta{Text: "Raw is answering"},
		events.JobFinished{ID: "job", Summary: "The fixture has three files.", Envelope: dylan, SpeechPending: true},
		events.ResponseReady{Response: "Raw's answer.", Envelope: &events.ResponseEnvelope{PersonaID: "raw", DisplayName: "Raw"}},
		events.JobResultDelivered{ID: "job", Summary: "The fixture has three files.", Envelope: dylan, Spoken: true},
	}}
	h.model.turnState = turnVoiceListening
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(output.String())
	reply, result := strings.Index(plain, "Raw's answer."), strings.Index(plain, "three files")
	if reply < 0 || result < 0 || result < reply {
		t.Fatalf("result drawn at %d, reply at %d; want the reply first:\n%s", result, reply, plain)
	}
}

type spokenReplyTerminal struct {
	model conversationModel
	steps []events.Event
}

func (h *spokenReplyTerminal) next() tea.Cmd {
	if len(h.steps) == 0 {
		return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return tea.QuitMsg{} })
	}
	e := h.steps[0]
	h.steps = h.steps[1:]
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg { return e })
}
func (h *spokenReplyTerminal) Init() tea.Cmd { return h.next() }
func (h *spokenReplyTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if e, ok := msg.(events.Event); ok {
		h.model.handleEvent(e)
		return h, h.next()
	}
	return h, nil
}
func (h *spokenReplyTerminal) View() string { return h.model.View() }
