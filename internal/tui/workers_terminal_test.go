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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// Runs the real App, worker table and Bubble Tea renderer without models, mic,
// networking or the user's install. Opt in to a keyboard-driven PTY with:
// Build with go test -c, then run that test binary directly in a terminal with
// OBEY_VOICE_WORKERS_TERMINAL=1 and -test.run '^TestWorkersTerminal$' -test.v.
// The go test runner itself does not forward interactive stdin to the binary.
func TestWorkersTerminal(t *testing.T) {
	path := realWriterInstall(t, "voice_workers_disabled: false\n")
	cfg := &config.Config{AgentName: "Workers test", BrainProvider: "ollama"}
	p := &pipeline.Pipeline{Events: events.NewBus()}
	table := job.NewTable(t.Context())
	t.Cleanup(table.AbortAll)
	agency := pipeline.NewAgency(p, table, nil, cfg, nil)
	agency.Wire()
	started := make(chan struct{})
	_, err := table.Start(t.Context(), job.Spec{Task: "Review campaign changes", Provider: "ollama", Model: "test-local",
		Run: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_, err = table.Start(t.Context(), job.Spec{Task: "Write a summary", Provider: "ollama", Model: "test-local",
		Run: func(context.Context) (string, error) { t.Error("stop all launched queued work"); return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(cfg, nil, nil)
	app.width, app.height = 100, 26
	app.screen = screenConversation
	app.conversation = sizedConversation(t, 100, 26)
	app.conversation.ready = true
	app.conversation.cfg = cfg
	app.runtime = &ConversationRuntime{Pipeline: p}
	model, initial := app.Update(switchScreenMsg(screenWorkers))
	interactive := os.Getenv("OBEY_VOICE_WORKERS_TERMINAL") == "1"
	harness := &workersTerminal{app: model.(App), initial: initial, scripted: !interactive}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if interactive {
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(harness, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	if !interactive {
		for _, want := range []string{"Review campaign changes", "ollama / test-local", "Allow background workers:", "Stop requested", "Termination", "unknown"} {
			if !strings.Contains(stripANSI(output.String()), want) {
				t.Fatalf("renderer missing %q:\n%s", want, stripANSI(output.String()))
			}
		}
		if p.WorkersEnabled() || table.Active() {
			t.Fatal("terminal controls did not disable and cancel work")
		}
		loaded, err := config.LoadRaw()
		if err != nil || !loaded.VoiceWorkersDisabled {
			t.Fatal("terminal toggle was not persisted")
		}
	}
	t.Logf("isolated persisted config:\n%s", readWritten(t, path))
}

type workersTerminalStep int
type workersTerminal struct {
	app      App
	initial  tea.Cmd
	scripted bool
}

func (h *workersTerminal) Init() tea.Cmd {
	if !h.scripted {
		return h.initial
	}
	return tea.Batch(h.initial, h.step(0))
}
func (h *workersTerminal) step(n int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return workersTerminalStep(n) })
}
func (h *workersTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if step, ok := msg.(workersTerminalStep); ok {
		keys := []tea.KeyMsg{
			{Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyEnter},
			{Type: tea.KeyRunes, Runes: []rune("t")},
			{Type: tea.KeyRunes, Runes: []rune("x")},
			{Type: tea.KeyRunes, Runes: []rune("y")},
		}
		if int(step) >= len(keys) {
			return h, tea.Quit
		}
		msg = keys[step]
		next = h.step(int(step) + 1)
	}
	model, cmd := h.app.Update(msg)
	h.app = model.(App)
	return h, tea.Batch(cmd, next)
}
func (h *workersTerminal) View() string { return h.app.View() }

// Render running activity, refresh it to a failure, and scroll to the final
// result using the actual Bubble Tea event loop and terminal renderer.
func TestWorkerActivityTerminal(t *testing.T) {
	controller := &testWorkers{items: []pipeline.WorkerInfo{{ID: "fixture", Task: "Check the project", Provider: "codex", Model: "fixture-model", State: "running",
		Thread: []brain.Turn{{Role: "user", Content: "Check the project"}, {Role: "assistant", Content: "I am checking the test suite."}},
		Tools:  []workfold.Call{{Name: "commandExecution", Summary: "go test ./...", Status: "running"}}}}}
	m := newWorkers(&config.Config{}, controller)
	m.width, m.height, m.cursor = 88, 26, 2
	h := &workerActivityTerminal{model: m, controller: controller}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(output.String())
	for _, want := range []string{"codex / fixture-model", "go test ./...", "Tool activity", "Worker thread", "running", "failed", "exit 1", "One check failed."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("terminal missing %q:\n%s", want, plain)
		}
	}
}

type workerActivityTerminal struct {
	model      workersModel
	controller *testWorkers
}

func (h *workerActivityTerminal) Init() tea.Cmd { return h.next(0) }
func (h *workerActivityTerminal) next(n int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return workersTerminalStep(n) })
}
func (h *workerActivityTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if step, ok := msg.(workersTerminalStep); ok {
		switch step {
		case 0:
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyHome})
		case 1:
			item := &h.controller.items[0]
			item.State, item.Result = "done", "One check failed."
			item.Thread = append(item.Thread, brain.Turn{Role: "assistant", Content: item.Result})
			item.Tools[0].Status, item.Tools[0].Err, item.Tools[0].Preview = "failed", "exit 1", "FAIL project checks"
			h.model, _ = h.model.Update(workersTickMsg{h.model.token})
		case 2:
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyEnd})
		default:
			return h, tea.Quit
		}
		return h, h.next(int(step) + 1)
	}
	return h, nil
}
func (h *workerActivityTerminal) View() string { return h.model.View() }
