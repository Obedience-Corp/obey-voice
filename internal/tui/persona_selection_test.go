package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestPersonasInChatSelectionQueuesFIFOAndStopsOnError(t *testing.T) {
	m := newConversation("Original")
	m.deps.ctx = context.Background()
	m.sessionID = "session"
	var calls []string
	rt := &ConversationRuntime{}
	rt.SelectPersona = func(_ context.Context, id string) (events.PersonaSelection, *config.Config, error) {
		calls = append(calls, id)
		if id == "santa" {
			return events.PersonaSelection{}, nil, errors.New("prepare failed")
		}
		return events.PersonaSelection{PersonaID: id, DisplayName: "Samantha", Revision: 2}, &config.Config{AgentName: "Samantha"}, nil
	}
	m.configureSelection(rt)
	m.turnState = turnTextRunning
	if cmd := m.enqueuePersonaSelection("santa"); cmd != nil {
		t.Fatal("selection started while a turn was active")
	}
	if cmd := m.enqueuePersonaSelection("samantha"); cmd != nil {
		t.Fatal("second selection started while a turn was active")
	}
	if len(m.selectionQueue) != 2 {
		t.Fatalf("queue = %v, want two requests", m.selectionQueue)
	}

	m.turnState = turnIdle
	cmd := m.startNextPersonaSelection()
	if cmd == nil {
		t.Fatal("unblocked queue did not start")
	}
	first := cmd()
	msg, ok := first.(personaSelectionResultMsg)
	if !ok {
		t.Fatalf("first result = %T", first)
	}
	accepted, next := m.finishPersonaSelection(msg)
	if !accepted {
		t.Fatal("current selection result was rejected")
	}
	if next == nil {
		t.Fatal("FIFO queue stopped after the first selection error")
	}
	second := next()
	msg, ok = second.(personaSelectionResultMsg)
	if !ok {
		t.Fatalf("second result = %T", second)
	}
	accepted, next = m.finishPersonaSelection(msg)
	if !accepted || next != nil {
		t.Fatalf("second result accepted=%v next=%v", accepted, next)
	}
	if got, want := strings.Join(calls, ","), "santa,samantha"; got != want {
		t.Fatalf("selection order = %q, want %q", got, want)
	}
	if len(m.selectionQueue) != 0 || m.selectionBusy {
		t.Fatalf("selection queue not drained: queue=%v busy=%v", m.selectionQueue, m.selectionBusy)
	}
}

func TestConversationResponseUsesEnvelopeDisplayName(t *testing.T) {
	m := newConversation("Current")
	m.handleEvent(events.ResponseReady{
		Response: "A reply from the previous binding",
		Envelope: &events.ResponseEnvelope{DisplayName: "Previous"},
	})
	if len(m.items) != 1 || m.items[0].name != "Previous" {
		t.Fatalf("reply identity = %+v, want Previous", m.items)
	}
	m.handleEvent(events.PersonaSelection{DisplayName: "Current", Revision: 1})
	if m.agentName != "Current" {
		t.Fatalf("selection did not update current display: %q", m.agentName)
	}
}

func TestConversationSelectionIgnoresLateRevision(t *testing.T) {
	m := newConversation("Original")
	m.handleEvent(events.PersonaSelection{PersonaID: "samantha", DisplayName: "Samantha", BindingHash: "new", Revision: 4})
	m.handleEvent(events.PersonaSelection{PersonaID: "santa", DisplayName: "Santa", BindingHash: "old", Revision: 3})
	if m.agentName != "Samantha" || m.selection.PersonaID != "samantha" || m.selectionRevision != 4 {
		t.Fatalf("late selection regressed state: name=%q selection=%+v revision=%d", m.agentName, m.selection, m.selectionRevision)
	}
}

func TestConversationSelectionIgnoresPreviousSessionEvents(t *testing.T) {
	m := newConversation("Samantha")
	m.sessionID = "current"
	m.handleEvent(events.PersonaSelection{SessionID: "current", PersonaID: "samantha", DisplayName: "Samantha", Revision: 1})
	m.handleEvent(events.PersonaSelection{SessionID: "previous", PersonaID: "santa", DisplayName: "Santa", Revision: 90})
	if m.agentName != "Samantha" || m.selectionRevision != 1 {
		t.Fatalf("previous session changed responder: %+v", m.selection)
	}
}

func TestConversationSelectionRejectsResultFromRetiredRuntime(t *testing.T) {
	m := newConversation("Original")
	m.deps.ctx = context.Background()
	m.sessionID = "session"
	old := &ConversationRuntime{}
	old.SelectPersona = func(context.Context, string) (events.PersonaSelection, *config.Config, error) {
		return events.PersonaSelection{PersonaID: "old", DisplayName: "Old", Revision: 1}, &config.Config{AgentName: "Old"}, nil
	}
	m.configureSelection(old)
	cmd := m.enqueuePersonaSelection("old")
	if cmd == nil {
		t.Fatal("old runtime selection did not start")
	}
	result := cmd().(personaSelectionResultMsg)
	newRuntime := &ConversationRuntime{}
	m.configureSelection(newRuntime)
	accepted, _ := m.finishPersonaSelection(result)
	if accepted {
		t.Fatal("retired runtime selection was accepted")
	}
	if m.selection.PersonaID != "" || m.selectionBusy || len(m.selectionQueue) != 0 {
		t.Fatalf("stale result changed owner state: selection=%+v busy=%v queue=%v", m.selection, m.selectionBusy, m.selectionQueue)
	}
}

func TestConversationSelectionWaitsForTurnCompletion(t *testing.T) {
	m := newConversation("Original")
	m.deps.ctx = context.Background()
	m.sessionID = "session"
	called := make(chan struct{}, 1)
	rt := &ConversationRuntime{}
	rt.SelectPersona = func(context.Context, string) (events.PersonaSelection, *config.Config, error) {
		called <- struct{}{}
		return events.PersonaSelection{PersonaID: "santa", DisplayName: "Santa", Revision: 1}, &config.Config{AgentName: "Santa"}, nil
	}
	m.configureSelection(rt)
	m.turnState = turnTextRunning
	if cmd := m.enqueuePersonaSelection("santa"); cmd != nil {
		t.Fatal("selection started before the turn completed")
	}
	select {
	case <-called:
		t.Fatal("selection callback ran while the turn was active")
	default:
	}
	cmd := m.handleTextTurnDone(textTurnDoneMsg{})
	if cmd == nil {
		t.Fatal("turn completion did not release queued selection")
	}
	_ = cmd()
	select {
	case <-called:
	default:
		t.Fatal("released selection callback did not run")
	}
}

func TestConversationSelectionQueueSurvivesPersonasReentry(t *testing.T) {
	app := App{screen: screenPersonas, cfg: &config.Config{}, runCtx: context.Background()}
	app.conversation = newConversation("Original")
	app.conversation.deps.ctx = context.Background()
	app.conversation.sessionID = "session"
	rt := &ConversationRuntime{}
	rt.SelectPersona = func(context.Context, string) (events.PersonaSelection, *config.Config, error) {
		return events.PersonaSelection{PersonaID: "santa", DisplayName: "Santa", Revision: 1}, &config.Config{AgentName: "Santa"}, nil
	}
	app.runtime = rt
	app.conversation.configureSelection(rt)
	app.personas = newPersonas(app.cfg)
	app.personas.returnScreen = screenConversation
	app.conversation.turnState = turnTextRunning
	model, _ := app.Update(personaSelectionRequestMsg{id: "santa"})
	app = model.(App)
	if len(app.conversation.selectionQueue) != 1 {
		t.Fatalf("queued selection lost: %v", app.conversation.selectionQueue)
	}
	model, _ = app.Update(switchScreenMsg(screenConversation))
	app = model.(App)
	model, _ = app.Update(switchScreenMsg(screenPersonas))
	app = model.(App)
	if len(app.conversation.selectionQueue) != 1 || app.conversation.selectionBusy {
		t.Fatalf("reentry changed owner queue: queue=%v busy=%v", app.conversation.selectionQueue, app.conversation.selectionBusy)
	}
}

func TestPersonaSelectionEventUpdatesRuntimeIdentityAndConfig(t *testing.T) {
	cfg := &config.Config{AgentName: "Santa"}
	selection := events.PersonaSelection{PersonaID: "santa", DisplayName: "Santa", Revision: 3}
	rt := &ConversationRuntime{
		PersonaID:         "old",
		AgentName:         "Old",
		SessionCfg:        &config.Config{AgentName: "Old"},
		SelectionSnapshot: func() events.PersonaSelection { return selection },
		SelectionConfig:   func() *config.Config { return cfg },
	}
	app := App{screen: screenConversation, runtime: rt, conversation: newConversation("Old")}
	model, _ := app.Update(busEventMsg{event: selection})
	app = model.(App)
	if rt.PersonaID != "santa" || rt.AgentName != "Santa" || rt.SessionCfg != cfg || app.conversation.cfg != cfg {
		t.Fatalf("runtime selection not synchronized: persona=%q name=%q cfg=%p conversation=%p", rt.PersonaID, rt.AgentName, rt.SessionCfg, app.conversation.cfg)
	}
}

func TestAppIgnoresStalePersonaSelectionResult(t *testing.T) {
	current := &ConversationRuntime{PersonaID: "current", AgentName: "Current"}
	app := App{screen: screenConversation, runtime: current, conversation: newConversation("Current")}
	app.conversation.deps.ctx = context.Background()
	app.conversation.sessionID = "session"
	app.conversation.configureSelection(current)
	old := &ConversationRuntime{PersonaID: "old", AgentName: "Old"}
	model, _ := app.Update(personaSelectionResultMsg{
		id: "old", runtime: old, sessionID: "session", runtimeEpoch: 1, requestSequence: 1,
		selection: events.PersonaSelection{PersonaID: "old", DisplayName: "Old", Revision: 9},
		cfg:       &config.Config{AgentName: "Old"},
	})
	app = model.(App)
	if current.PersonaID != "current" || current.AgentName != "Current" || app.conversation.agentName != "Current" {
		t.Fatalf("stale result published: runtime=%+v name=%q", current, app.conversation.agentName)
	}
}
