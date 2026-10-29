package tui

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// Compile with go test -c. Run in a PTY with
// OBEY_VOICE_WORKER_THREAD_TERMINAL=1 ./tui.test -test.run '^TestWorkerChatTerminal$'.
// Ctrl+R completes the fixture; Ctrl+C exits. No models, commands or audio run.
func TestWorkerChatTerminal(t *testing.T) {
	realWriterInstall(t, "voice_workers_disabled: false\n")
	controller := &testWorkers{items: []pipeline.WorkerInfo{workerChatFixture()}}
	m := newWorkers(&config.Config{}, controller)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 88, Height: 30})
	m.openDetail("chat-fixture")
	interactive := os.Getenv("OBEY_VOICE_WORKER_THREAD_TERMINAL") == "1"
	h := &workerThreadTerminal{model: m, controller: controller, interactive: interactive}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if interactive {
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(h, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	if !interactive {
		for _, want := range []string{"Worker conversation", "› Request", "● Worker", "run_command", "Arguments", "Outcome not reported", "exit 1", "One check failed."} {
			if !strings.Contains(stripANSI(output.String()), want) {
				t.Fatalf("terminal did not render %q:\n%s", want, stripANSI(output.String()))
			}
		}
	}
}

type workerThreadTerminal struct {
	model       workersModel
	controller  *testWorkers
	interactive bool
}

func (h *workerThreadTerminal) Init() tea.Cmd {
	if h.interactive {
		return nil
	}
	return h.next(0)
}

func (h *workerThreadTerminal) next(n int) tea.Cmd {
	return tea.Tick(75*time.Millisecond, func(time.Time) tea.Msg { return workersTerminalStep(n) })
}

func (h *workerThreadTerminal) finish() {
	item := &h.controller.items[0]
	item.State, item.Result = "done", "One check failed.\nThe command exited with code 1; inspect the failing test before retrying."
	item.Tools[1].Status, item.Tools[1].Err, item.Tools[1].Preview = "failed", "exit 1", "FAIL project checks"
	item.Thread = append(item.Thread, brain.Turn{Role: "tool", ToolCallID: "tests", Content: "FAIL project checks"}, brain.Turn{Role: "assistant", Content: item.Result})
	h.model.refresh()
}

func (h *workerThreadTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if step, ok := msg.(workersTerminalStep); ok {
		switch step {
		case 0:
			msg = tea.KeyMsg{Type: tea.KeyHome}
		case 1:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}
		case 2:
			msg = tea.KeyMsg{Type: tea.KeyHome}
		case 3:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}
		case 4:
			h.finish()
			msg = tea.KeyMsg{Type: tea.KeyEnd}
		default:
			return h, tea.Quit
		}
		next = h.next(int(step) + 1)
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.Type == tea.KeyCtrlC {
			return h, tea.Quit
		}
		if key.Type == tea.KeyCtrlR {
			h.finish()
			return h, nil
		}
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, tea.Batch(cmd, next)
}

func (h *workerThreadTerminal) View() string { return h.model.View() }
