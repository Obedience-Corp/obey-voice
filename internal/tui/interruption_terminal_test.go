package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestInterruptedPlaybackRemainsVisibleAfterListeningResumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	bus := events.NewBus()
	bridge := newEventBridge(16)
	bridge.attach(bus)
	model := sizedConversation(t, 120, 30)
	model.bridge, model.deps.ctx = bridge, ctx
	h := &interruptionTerminalHarness{model: model, ctx: ctx}
	for _, e := range []events.Event{
		events.UserInput{Text: "Tell me about it."},
		events.SpeakingStarted{Text: "This reply was cut short.", AudibleSink: "local_speaker"},
		events.SpeakingInterrupted{Reason: "barge_in"},
		events.TurnInterrupted{Reason: "barge_in"},
		events.SpeakingComplete{Interrupted: true, AudibleSink: "local_speaker"},
		events.ResponseReady{Response: "This reply was cut short.", Interrupted: true},
		events.TurnMetrics{Outcome: "interrupted", Interrupted: true, AudibleSink: "local_speaker", PlaybackCompleteElapsed: 4600 * time.Millisecond},
		events.STTPhase{Phase: "listening"},
	} {
		bus.Emit(e)
	}
	var terminal bytes.Buffer
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.chat, "playback stopped 4.6s") || !strings.Contains(h.chat, "interrupted") || strings.Contains(h.chat, "playback done") {
		t.Fatalf("cutoff hidden after listening resumed:\n%s", h.chat)
	}
	if !strings.Contains(h.activity, "interrupted: barge_in") || !strings.Contains(stripANSI(terminal.String()), "barge_in") {
		t.Fatalf("interruption reason absent from terminal activity:\n%s", h.activity)
	}
}

type interruptionTerminalHarness struct {
	model          conversationModel
	ctx            context.Context
	chat, activity string
}

func (h *interruptionTerminalHarness) Init() tea.Cmd { return h.model.bridge.waitContext(h.ctx) }
func (h *interruptionTerminalHarness) View() string  { return h.model.View() }
func (h *interruptionTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if bridged, ok := msg.(busEventMsg); ok {
		if e, ok := bridged.event.(events.STTPhase); ok && e.Phase == "listening" {
			h.chat = stripANSI(h.model.View())
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			h.activity = stripANSI(h.model.View())
			return h, tea.Quit
		}
	}
	return h, cmd
}
