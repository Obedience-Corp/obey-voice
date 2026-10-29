package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

type contextTerminalHarness struct {
	model  conversationModel
	views  chan string
	mu     sync.Mutex
	output bytes.Buffer
}

func (h *contextTerminalHarness) Init() tea.Cmd { return nil }
func (h *contextTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := h.model.Update(msg)
	h.model = m
	if _, ok := msg.(busEventMsg); ok {
		h.views <- stripANSI(m.View())
	}
	return h, cmd
}
func (h *contextTerminalHarness) View() string { return h.model.View() }
func (h *contextTerminalHarness) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.output.Write(p)
}

func TestContextMeterTerminalLifecycle(t *testing.T) {
	m := sizedConversation(t, 90, 24)
	m.cfg = &config.Config{BrainProvider: "ollama", OllamaNumCtx: 32768}
	h := &contextTerminalHarness{model: m, views: make(chan string, 4)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		p.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("terminal did not stop")
		}
	}()
	send := func(event events.Event, want string) {
		t.Helper()
		p.Send(busEventMsg{event: event})
		select {
		case view := <-h.views:
			if !strings.Contains(view, want) {
				t.Fatalf("missing %q:\n%s", want, view)
			}
			t.Logf("%s", view)
		case <-ctx.Done():
			t.Fatal("terminal update timed out")
		}
	}
	send(events.ContinuityBudget{EstTokens: 7424, NumCtx: 8192, SoftLimit: 6553, HardLimit: 7372}, "7.4k / 8k")
	send(events.CompactionProgress{Stage: "summarize"}, "compact summarize")
	// Wait for the actual renderer to flush this intermediate frame.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.Lock()
		rendered := h.output.String()
		h.mu.Unlock()
		if strings.Contains(rendered, "compact summarize") {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("progress did not reach terminal renderer")
		}
	}
	send(events.ContinuityBudget{EstTokens: 1000, NumCtx: 8192}, "1.0k / 8k")
	send(events.CompactionProgress{Stage: "canceled"}, "1.0k / 8k")
}

func TestMemorySettingsTerminalPersistence(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BrainProvider = "ollama"
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionBrain, 100, 35
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		p.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("terminal did not stop")
		}
	}()
	key := func(k tea.KeyType) workerTerminalState {
		p.Send(tea.KeyMsg{Type: k})
		select {
		case state := <-h.states:
			return state
		case <-ctx.Done():
			t.Fatal("settings key timed out")
			return workerTerminalState{}
		}
	}
	state := key(tea.KeyHome)
	for i := 0; i < 100 && state.key != "ollama_num_ctx"; i++ {
		state = key(tea.KeyDown)
	}
	if state.key != "ollama_num_ctx" {
		t.Fatal("memory preset unreachable")
	}
	for _, want := range []int{8192, 16384, 32768} {
		state = key(tea.KeyEnter)
		if !strings.Contains(state.message, "saved") {
			t.Fatalf("save failed: %s", state.message)
		}
		loaded, err := config.LoadWithoutHooks()
		if err != nil {
			t.Fatal(err)
		}
		if loaded.OllamaNumCtx != want || loaded.ContinuityNumCtxCeil != want {
			t.Fatalf("persisted %d/%d, want %d", loaded.OllamaNumCtx, loaded.ContinuityNumCtxCeil, want)
		}
	}
}
