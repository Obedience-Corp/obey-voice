package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// TestToolOutcomeProductionTerminalProof follows one real routed worker from
// the Ollama provider through shell execution, Agency hooks, the Bubble Tea
// bridge, and the durable transcript. The fake only replaces the model HTTP
// service; it does not manufacture tool events or command results.
func TestToolOutcomeProductionTerminalProof(t *testing.T) {
	dir := t.TempDir()
	var chats atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"proof"}]}`)
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`)
		case "/api/chat":
			n := chats.Add(1)
			var body struct {
				Messages []api.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var line string
			switch n {
			case 1:
				line = `{"model":"proof","message":{"role":"assistant","tool_calls":[{"id":"cmd-1","function":{"name":"run_command","arguments":{"command":"printf '{\\\"ok\\\":false}' ; exit 7"}}}]},"done":true}`
			case 2:
				line = `{"model":"proof","message":{"role":"assistant","tool_calls":[{"id":"cmd-2","function":{"name":"run_command","arguments":{"command":"printf '{\\\"http\\\":true}'"}}}]},"done":true}`
			default:
				line = `{"model":"proof","message":{"role":"assistant","content":"Worker finished both commands."},"done":true,"done_reason":"stop"}`
			}
			_, _ = io.WriteString(w, line+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer model.Close()
	base, err := url.Parse(model.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BrainProvider:       "ollama",
		OllamaHost:          base.String(),
		OllamaModel:         "proof",
		WorkDir:             dir,
		VoiceToolsEnabled:   true,
		ToolCommandTimeout:  10,
		VoiceWorkerProvider: "ollama",
		VoiceWorkerModel:    "proof",
	}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatalf("NewOllama(fake): %v", err)
	}
	bus := events.NewBus()
	logPath := dir + "/session.jsonl"
	writer, err := transcript.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	writer.Attach(bus)
	defer writer.Close()
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(1))
	defer jobs.Close()
	p := &pipeline.Pipeline{Brain: live, Events: bus, VoiceToolsEnabled: true}
	agency := pipeline.NewAgency(p, jobs, live, cfg, nil)
	agency.Wire()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	m := sizedConversation(t, 120, 30)
	bridge := newEventBridge(64)
	bridge.attach(bus)
	m.bridge = bridge
	m.deps.ctx = ctx
	h := &toolOutcomeTerminalHarness{model: m, ctx: ctx}
	var terminal bytes.Buffer
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
	turnErr := make(chan error, 1)
	go func() {
		turnErr <- p.RunTurnTextMode(ctx, "run this command and report the result")
	}()
	if _, err := program.Run(); err != nil {
		t.Fatalf("terminal program: %v", err)
	}
	if err := <-turnErr; err != nil {
		t.Fatalf("production routed turn: %v", err)
	}
	if chats.Load() != 3 {
		t.Fatalf("fake model chat calls = %d, want two tool calls and one final reply", chats.Load())
	}
	if !strings.Contains(h.chat, "run this command and report the result") {
		t.Fatalf("original conversation missing from chat view:\n%s", h.chat)
	}
	if !strings.Contains(h.activity, "failed") || !strings.Contains(h.activity, "7") {
		t.Fatalf("failed command outcome missing from Activity:\n%s", h.activity)
	}
	terminalText := stripANSI(terminal.String())
	if !strings.Contains(terminalText, "failed") || !strings.Contains(terminalText, "7") {
		t.Fatalf("failed command outcome missing from terminal output:\n%s", terminalText)
	}
	if h.jobStarted != 1 || h.jobFinished != 1 || h.jobToolCalls != 2 {
		t.Fatalf("job lifecycle/fold = started %d finished %d tool calls %d, want 1/1/2", h.jobStarted, h.jobFinished, h.jobToolCalls)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("invalid JSONL record: %v", err)
		}
		if record["type"] == "tool_result" {
			results = append(results, record)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("persisted tool results = %d, want two calls in one worker", len(results))
	}
	first := results[0]
	if first["job_id"] == nil || first["status"] != "failed" || first["exit_code"] != float64(7) {
		t.Fatalf("persisted nonzero command outcome = %#v", first)
	}
	if _, ok := first["http_status"]; ok {
		t.Fatalf("process command incorrectly claimed HTTP status: %#v", first)
	}
	if second := results[1]; second["job_id"] != first["job_id"] {
		t.Fatalf("tool calls split across workers: first=%#v second=%#v", first, second)
	} else if _, ok := second["http_status"]; ok {
		t.Fatalf("generic command incorrectly claimed HTTP status: %#v", second)
	} else if second["status"] != "succeeded" || second["exit_code"] != float64(0) {
		t.Fatalf("persisted successful command outcome = %#v", second)
	}
}

type toolOutcomeTerminalHarness struct {
	model        conversationModel
	ctx          context.Context
	chat         string
	activity     string
	terminal     string
	jobStarted   int
	jobFinished  int
	jobToolCalls int
}

func (h *toolOutcomeTerminalHarness) Init() tea.Cmd { return h.model.bridge.waitContext(h.ctx) }

func (h *toolOutcomeTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if bridged, ok := msg.(busEventMsg); ok {
		switch e := bridged.event.(type) {
		case events.JobStarted:
			h.jobStarted++
		case events.ToolCallFinished:
			if e.JobID != "" {
				h.jobToolCalls++
			}
		case events.JobFinished:
			h.jobFinished++
			h.chat = stripANSI(h.model.View())
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			h.activity = stripANSI(h.model.View())
			h.terminal = h.activity + "\n" + h.chat
			return h, tea.Quit
		}
	}
	return h, cmd
}

func (h *toolOutcomeTerminalHarness) View() string { return h.model.View() }
