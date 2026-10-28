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

// Drive the production conversation through the real Bubble Tea renderer.
// Tool evidence must remain in chat after the job fails, without turning
// background tool events into assistant speech or replacing live status.
func TestWorkerToolChatTerminal(t *testing.T) {
	m := sizedConversation(t, 100, 32)
	m.ready = true
	m.setStatus("Listening", false)
	h := &workerChatTerminal{model: m}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read_file", "missing.txt", "run_command", "go test ./...", "running", "failed", "exit status 1", "FAIL checks", "No final answer", "Listening"} {
		if !strings.Contains(stripANSI(output.String()), want) {
			t.Fatalf("terminal missing %q:\n%s", want, stripANSI(output.String()))
		}
	}
	lines := jobLines(h.model)
	if len(lines) != 1 {
		t.Fatalf("worker created duplicate items: %v", lines)
	}
	for _, want := range []string{"read_file", "missing.txt", "run_command", "go test ./...", "exit status 1", "No final answer"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("completion dropped %q: %v", want, lines)
		}
	}
	if h.model.status != "Listening" {
		t.Fatalf("worker stole live status: %q", h.model.status)
	}
}

type workerChatTerminal struct{ model conversationModel }
type workerChatStep int

func (h *workerChatTerminal) next(n int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return workerChatStep(n) })
}
func (h *workerChatTerminal) Init() tea.Cmd { return h.next(0) }
func (h *workerChatTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	step, ok := msg.(workerChatStep)
	if !ok {
		return h, nil
	}
	var e events.Event
	switch step {
	case 0:
		e = events.JobStarted{ID: "job-fixture", Task: "Inspect project"}
	case 1:
		e = events.ToolCallStarted{JobID: "job-fixture", ID: "read", Name: "read_file", Kind: "read", Summary: "missing.txt"}
	case 2:
		e = events.ToolCallFinished{JobID: "job-fixture", ID: "read", Name: "read_file", Kind: "read", Err: "error: missing.txt does not exist"}
	case 3:
		e = events.ToolCallStarted{JobID: "job-fixture", ID: "command", Name: "run_command", Kind: "execute", Summary: "go test ./..."}
	case 4:
		e = events.ToolCallFinished{JobID: "job-fixture", ID: "command", Name: "run_command", Kind: "execute", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeFailed, Reason: "exit status 1", OutputPreview: "FAIL checks"}}
	case 5:
		e = events.JobFailed{ID: "job-fixture", Task: "Inspect project", Err: "No final answer"}
	default:
		return h, tea.Quit
	}
	h.model, _ = h.model.Update(busEventMsg{event: e})
	return h, h.next(int(step) + 1)
}
func (h *workerChatTerminal) View() string { return h.model.View() }
