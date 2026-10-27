package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

// Exercise the actual bus bridge, Bubble Tea loop and renderer. Error text
// remains in Chat after the status is replaced by the next turn, and the same
// notice is carried in the desktop snapshot without becoming assistant speech.
func TestErrorRemainsVisibleInTerminalAndDesktop(t *testing.T) {
	for _, partial := range []string{"", "The part that completed."} {
		t.Run(partial, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			bus := events.NewBus()
			bridge := newEventBridge(16)
			bridge.attach(bus)
			m := sizedConversation(t, 120, 30)
			m.bridge, m.deps.ctx = bridge, ctx
			h := &errorTerminalHarness{model: m, ctx: ctx}
			bus.Emit(events.UserInput{Text: "Please check that."})
			bus.Emit(events.Error{Stage: "brain", Message: "cursor: authentication expired"})
			bus.Emit(events.ResponseReady{Response: partial, Degraded: true})
			bus.Emit(events.ThinkingStarted{}) // clears status, but must not erase the error
			var output bytes.Buffer
			p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler())
			if _, err := p.Run(); err != nil {
				t.Fatal(err)
			}
			rendered := stripANSI(output.String())
			if !strings.Contains(rendered, "Error: [brain] cursor: authentication expired") || strings.Contains(rendered, "no reply") || strings.Contains(rendered, "simpler approach") {
				t.Fatalf("terminal lost useful error: %s", rendered)
			}
			state := (desktopModel{app: App{conversation: h.model}, rt: fakeRuntime()}).state()
			if len(state.Notices) != 1 || !strings.Contains(state.Notices[0], "authentication expired") {
				t.Fatalf("desktop notices=%v", state.Notices)
			}
			assistant := 0
			for _, turn := range state.Turns {
				if turn.Role == "assistant" {
					assistant++
					if turn.Content != partial {
						t.Fatalf("fabricated assistant turn: %+v", turn)
					}
				}
			}
			if (partial == "" && assistant != 0) || (partial != "" && assistant != 1) {
				t.Fatalf("assistant turns=%d", assistant)
			}
		})
	}
}

type errorTerminalHarness struct {
	model conversationModel
	ctx   context.Context
}

func (h *errorTerminalHarness) Init() tea.Cmd { return h.model.bridge.waitContext(h.ctx) }
func (h *errorTerminalHarness) View() string  { return h.model.View() }
func (h *errorTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if b, ok := msg.(busEventMsg); ok {
		if _, done := b.event.(events.ThinkingStarted); done {
			return h, tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tea.Quit() })
		}
	}
	return h, cmd
}
