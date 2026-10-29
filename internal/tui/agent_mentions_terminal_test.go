package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAgentMentionColorInTerminalAndReplay(t *testing.T) {
	workerTestColors(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	text := "Sure thing, Lance.\n@Venessa Hey Venessa! What should we cover?"
	styled := placeholderKnownStyle.Render("@Venessa")
	if styled == "@Venessa" {
		t.Fatal("test requires terminal colors")
	}
	m := sizedConversation(t, 120, 30)
	m.appendStreamingDelta(text)
	if !strings.Contains(m.View(), styled) {
		t.Fatal("streaming mention is not highlighted")
	}
	m.streamingAgent = ""
	m.seedTranscript([]brain.Turn{{Role: "assistant", Content: text, PersonaDisplayName: "Obey"}})
	if !strings.Contains(m.View(), styled) {
		t.Fatal("replayed mention is not highlighted")
	}
	m.clearTranscript()
	bus := events.NewBus()
	bridge := newEventBridge(16)
	bridge.attach(bus)
	m.bridge, m.deps.ctx = bridge, ctx
	h := &errorTerminalHarness{model: m, ctx: ctx}
	bus.Emit(events.ResponseReady{Response: text})
	bus.Emit(events.ThinkingStarted{})
	var output bytes.Buffer
	p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), styled) {
		t.Fatalf("terminal lost mention color: %q", output.String())
	}
	if h.model.lastAssistantText != text {
		t.Fatalf("copy text changed: %q", h.model.lastAssistantText)
	}
}
