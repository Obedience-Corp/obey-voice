package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

type workerTerminalState struct {
	key     string
	editing bool
	message string
}
type workerTerminalHarness struct {
	decisionResults chan string
	model           settingsModel
	states          chan workerTerminalState
	saved           chan *config.VoiceWorkers
	output          sync.Mutex
	text            bytes.Buffer
}

func (h *workerTerminalHarness) Init() tea.Cmd { return nil }
func (h *workerTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if saved, ok := msg.(workerSettingsSavedMsg); ok {
		h.saved <- saved.workers.Clone()
		return h, nil
	}
	m, cmd := h.model.Update(msg)
	h.model = m
	if _, ok := msg.(decisionTestMsg); ok && h.decisionResults != nil {
		h.decisionResults <- m.message
	}
	if _, ok := msg.(tea.KeyMsg); ok {
		row, _, _ := m.focused()
		h.states <- workerTerminalState{key: row.key, editing: m.edit.kind != editNone, message: m.message}
	}
	return h, cmd
}
func (h *workerTerminalHarness) View() string { return h.model.View() }
func (h *workerTerminalHarness) Write(p []byte) (int, error) {
	h.output.Lock()
	defer h.output.Unlock()
	return h.text.Write(p)
}

// Drive the production key handling and editor through a real Bubble Tea
// event/render loop. Persistence is the production CAS writer in a temp home.
func TestWorkerSettingsTerminalSaveAndCancel(t *testing.T) {
	for _, provider := range []string{"ollama", "cursor"} {
		t.Run(provider, func(t *testing.T) { testWorkerSettingsTerminal(t, provider) })
	}
}

func testWorkerSettingsTerminal(t *testing.T, provider string) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := tieredSettingsFixture()
	oldModel, newModel := "qwen3:8b", "qwen3:30b"
	if provider == "cursor" {
		oldModel, newModel = "auto", "composer-2.5"
		account := "default"
		cfg.VoiceWorkers.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: provider, Model: oldModel, Billing: config.WorkerBillingSubscription, AccountRef: &account}
	}

	committed, err := config.CommitVoiceWorkersCAS(cfg.VoiceWorkers, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VoiceWorkers = committed
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: provider, Available: true, Models: []string{oldModel, newModel}, WorkerModels: []string{oldModel, newModel}}})
	m.section, m.width, m.height = sectionWorker, 120, 45
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 1), saved: make(chan *config.VoiceWorkers, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		p.Quit()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("terminal did not stop")
		}
	}()
	key := func(msg tea.KeyMsg) workerTerminalState {
		p.Send(msg)
		select {
		case state := <-h.states:
			return state
		case <-ctx.Done():
			t.Fatal("terminal key timed out")
			return workerTerminalState{}
		}
	}
	navigate := func(want string) {
		state := key(tea.KeyMsg{Type: tea.KeyHome})
		for i := 0; i < 100 && state.key != want; i++ {
			state = key(tea.KeyMsg{Type: tea.KeyDown})
		}
		if state.key != want {
			t.Fatalf("could not reach %s; focused %s", want, state.key)
		}
	}
	edit := func() {
		navigate("worker_simple")
		state := key(tea.KeyMsg{Type: tea.KeyEnter})
		if !state.editing {
			state = key(tea.KeyMsg{Type: tea.KeyEnter})
		}
		if !state.editing {
			t.Fatal("tier assignment editor did not open")
		}
		key(tea.KeyMsg{Type: tea.KeyCtrlA})
		key(tea.KeyMsg{Type: tea.KeyCtrlK})
		key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(provider + "/" + newModel)})
		state = key(tea.KeyMsg{Type: tea.KeyEnter})
		if state.editing || !strings.Contains(state.message, "updated") {
			t.Fatalf("assignment editor: %+v", state)
		}
	}
	edit()
	navigate("worker_back")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	navigate("worker_cancel")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Revision != 1 || loaded.VoiceWorkers.Tiers.Simple.Assignment.Model != oldModel {
		t.Fatal("terminal Cancel persisted a draft")
	}
	edit()
	navigate("worker_back")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	navigate("worker_save")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case saved := <-h.saved:
		if saved.Revision != 2 || saved.Tiers.Simple.Revision != 2 {
			t.Fatalf("committed revision mismatch: %+v", saved)
		}
	case <-ctx.Done():
		t.Fatal("terminal Save did not complete")
	}
	loaded, err = config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Revision != 2 || loaded.VoiceWorkers.Tiers.Simple.Assignment.Model != newModel || loaded.VoiceWorkers.Tiers.Simple.Assignment.Provider != provider {
		t.Fatal("terminal Save disagrees with persisted configuration")
	}
}
