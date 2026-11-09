package tui

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

func TestProviderOutageTerminalAndDesktopStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	bus := events.NewBus()
	bridge := newEventBridge(16)
	bridge.attach(bus)
	m := sizedConversation(t, 120, 30)
	m.bridge, m.deps.ctx = bridge, ctx
	failure := events.NewProviderStatus("ollama", "brain", "model", "endpoint", "unavailable", "Brain unavailable. Restart Ollama, then /retry.")
	bus.Emit(events.Info{Message: failure.Message, ProviderStatus: failure})
	bus.Emit(events.STTPhase{Phase: "listening"})
	bus.Emit(events.AudioLevel{Source: "input", Level: .5})
	other := events.NewProviderStatus("ollama", "brain", "other", "endpoint", "available", "Other model works")
	bus.Emit(events.Info{Message: other.Message, ProviderStatus: other})
	bus.Emit(events.Info{Message: "inspect outage"})
	recovered := events.NewProviderStatus("ollama", "brain", "model", "endpoint", "available", "Model recovered")
	bus.Emit(events.Info{Message: recovered.Message, ProviderStatus: recovered})
	h := &providerTerminalHarness{model: m, ctx: ctx, t: t, failure: failure.Message}
	var terminal bytes.Buffer
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !h.inspected || !strings.Contains(stripANSI(terminal.String()), failure.Message) {
		t.Fatalf("outage not rendered: %s", terminal.String())
	}
}

type providerTerminalHarness struct {
	model     conversationModel
	ctx       context.Context
	t         *testing.T
	failure   string
	inspected bool
}

func (h *providerTerminalHarness) Init() tea.Cmd { return h.model.bridge.waitContext(h.ctx) }
func (h *providerTerminalHarness) View() string  { return h.model.View() }
func (h *providerTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if be, ok := msg.(busEventMsg); ok {
		if info, ok := be.event.(events.Info); ok {
			if info.Message == "inspect outage" {
				h.inspected = true
				header, _ := h.model.headerRight()
				if !strings.Contains(stripANSI(header), h.failure) {
					h.t.Errorf("listening erased outage: %s", header)
				}
				state := (desktopModel{app: App{conversation: h.model}, rt: fakeRuntime()}).state()
				if !state.Error || state.Status != h.failure {
					h.t.Errorf("desktop status lost outage: %+v", state)
				}
			}
			if info.Message == "Model recovered" {
				if h.model.providerWarning() != "" {
					h.t.Error("successful same-binding generation did not clear warning")
				}
				return h, tea.Quit
			}
		}
	}
	return h, cmd
}

func TestProviderStatusRejectsRetiredScopeAndPreservesSpeechFailure(t *testing.T) {
	m := sizedConversation(t, 100, 25)
	m.sessionID = "current"
	m.selection = events.PersonaSelection{PersonaID: "current-persona", ConversationEpoch: "current-epoch"}
	for _, trace := range []events.TraceContext{{SessionID: "old"}, {ConversationEpoch: "old"}, {PersonaID: pointerString("old-persona")}} {
		m.applyProviderStatus(events.NewProviderStatus("ollama", "brain", "m", "host", "unavailable", "late failure"), &trace)
	}
	if m.providerWarning() != "" {
		t.Fatal("retired failure changed current status")
	}
	speech := events.NewProviderStatus("openai-speech", "speech", "phonon", "host", "unavailable", "Primary speech unavailable")
	m.applyProviderStatus(speech, nil)
	m.applyProviderStatus(events.NewProviderStatus("ollama", "brain", "m", "host", "available", "Brain works"), nil)
	m.handleEvent(events.STTPhase{Phase: "listening"})
	if m.providerWarning() != speech.Message {
		t.Fatal("brain recovery healed primary speech")
	}
	m.handleEvent(events.ConversationCleared{})
	if m.providerWarning() != "" {
		t.Fatal("clear retained provider warning")
	}
}

func pointerString(s string) *string { return &s }

type inputRetryRunner struct {
	fakeTurnRunner
	retries atomic.Int32
}

func (r *inputRetryRunner) RetryLastTurn(ctx context.Context) error {
	r.retries.Add(1)
	return ctx.Err()
}

func TestInputRetryDrainsListeningAndDoesNotEchoInput(t *testing.T) {
	r := &inputRetryRunner{fakeTurnRunner: fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}}
	m, _ := startedConversation(t, &r.fakeTurnRunner, true)
	m.deps.runner = r
	voice := m.dispatchVoiceTurn()
	done := make(chan tea.Msg, 1)
	go func() { done <- voice() }()
	if cmd := m.requestInputRetry(); cmd != nil || r.retries.Load() != 0 {
		t.Fatal("retry raced active capture")
	}
	if m.requestInputRetry() != nil {
		t.Fatal("double activation dispatched")
	}
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listening did not drain")
	}
	m, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("drained capture did not dispatch retry")
	}
	_ = cmd()
	if r.retries.Load() != 1 || len(r.texts()) != 0 || len(m.plainTurns) != 0 {
		t.Fatalf("retries=%d turns=%v text=%v", r.retries.Load(), m.plainTurns, r.texts())
	}
}
