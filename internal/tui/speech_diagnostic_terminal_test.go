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

// Run the real bridge, update loop and renderer without microphone/model I/O.
// A handler-only test cannot detect a diagnostic omitted from bridge.attach.
func TestSpeechDiagnosticTerminalViews(t *testing.T) {
	for _, tc := range []struct {
		name       string
		diagnostic events.SpeechDiagnostic
		inChat     bool
	}{
		{
			name: "routine Kokoro observation",
			diagnostic: events.SpeechDiagnostic{
				Provider: "kokoro", Symptom: "playback_gap", Cause: "unknown",
				Message: "Playback gap 0.8s; cause unknown.",
			},
		},
		{
			name: "applicable advice",
			diagnostic: events.SpeechDiagnostic{
				Provider: "qwen3-tts", Symptom: "playback_gap", Cause: "synthesis",
				ActionCode: "whole_sentence", Message: "Whole-sentence playback may reduce these interruptions.",
			},
			inChat: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			bus := events.NewBus()
			bridge := newEventBridge(8)
			bridge.attach(bus)
			model := sizedConversation(t, 120, 30)
			model.bridge = bridge
			model.deps.ctx = ctx
			harness := &speechTerminalHarness{model: model, ctx: ctx}
			bus.Emit(events.ResponseReady{Response: "Conversation reply remains visible."})
			bus.Emit(tc.diagnostic)
			var terminal bytes.Buffer
			program := tea.NewProgram(harness, tea.WithInput(nil), tea.WithOutput(&terminal),
				tea.WithContext(ctx), tea.WithoutSignalHandler())
			if _, err := program.Run(); err != nil {
				t.Fatalf("terminal program did not receive/render the diagnostic: %v", err)
			}
			if !strings.Contains(harness.chat, "Conversation reply remains visible.") {
				t.Fatalf("diagnostic replaced the conversation reply:\n%s", harness.chat)
			}
			if got := strings.Contains(harness.chat, tc.diagnostic.Message); got != tc.inChat {
				t.Fatalf("diagnostic in Chat = %v, want %v:\n%s", got, tc.inChat, harness.chat)
			}
			if !strings.Contains(harness.activity, tc.diagnostic.Message) {
				t.Fatalf("diagnostic missing from Activity:\n%s", harness.activity)
			}
			if !strings.Contains(stripANSI(terminal.String()), tc.diagnostic.Message) {
				t.Fatalf("diagnostic missing from rendered terminal output:\n%s", terminal.String())
			}
		})
	}
}

type speechTerminalHarness struct {
	model    conversationModel
	ctx      context.Context
	chat     string
	activity string
}

func (h *speechTerminalHarness) Init() tea.Cmd {
	return h.model.bridge.waitContext(h.ctx)
}

func (h *speechTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if bridged, ok := msg.(busEventMsg); ok {
		if _, ok := bridged.event.(events.SpeechDiagnostic); ok {
			h.chat = stripANSI(h.model.View())
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			h.activity = stripANSI(h.model.View())
			return h, tea.Quit
		}
	}
	return h, cmd
}

func (h *speechTerminalHarness) View() string { return h.model.View() }
