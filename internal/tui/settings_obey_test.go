package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestObeySettingsTerminalPersistence(t *testing.T) {
	for _, key := range []string{"OBEY_PROVIDER", "OBEY_MODEL", "OBEY_CAMP", "OBEY_SOCKET", "OBEY_TOOL_POLICY"} {
		t.Setenv(key, "")
	}
	config.SetConfigDirForTest(t, t.TempDir())
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BrainProvider = "obey"
	cfg.ObeySocket = filepath.Join(t.TempDir(), "unavailable.sock")
	expectedSocket := filepath.Join(t.TempDir(), "selected.sock")
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionBrain, 110, 35
	m.visited[sectionBrain] = true
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		p.Quit()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	send := func(msg tea.KeyMsg) workerTerminalState {
		p.Send(msg)
		select {
		case state := <-h.states:
			return state
		case <-ctx.Done():
			t.Fatal("settings timed out")
			return workerTerminalState{}
		}
	}
	for _, field := range []struct{ key, value string }{{"obey_provider", "fixture-provider"}, {"obey_model", "fixture-model"}, {"obey_camp", "fixture-camp"}, {"obey_socket", expectedSocket}, {"obey_tool_policy", "daemon"}} {
		state := send(tea.KeyMsg{Type: tea.KeyHome})
		for i := 0; i < 100 && state.key != field.key; i++ {
			state = send(tea.KeyMsg{Type: tea.KeyDown})
		}
		if state.key != field.key {
			t.Fatalf("unreachable setting %s", field.key)
		}
		send(tea.KeyMsg{Type: tea.KeyEnter})
		send(tea.KeyMsg{Type: tea.KeyCtrlU})
		send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(field.value)})
		state = send(tea.KeyMsg{Type: tea.KeyEnter})
		if !strings.Contains(state.message, "saved") {
			t.Fatal(state.message)
		}
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ObeyProvider != "fixture-provider" || loaded.ObeyModel != "fixture-model" || loaded.ObeyCamp != "fixture-camp" || loaded.ObeySocket != expectedSocket || loaded.ObeyToolPolicy != "daemon" {
		t.Fatal("terminal edits were not persisted")
	}
}

func TestObeyOptionalUnavailableSelectionPreservesSavedProvider(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := config.SetAndSave("brain_provider", "ollama"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: "ollama", Available: true}, {Name: "obey", Available: false}})
	m.selectProviderOption(1)
	if m.cfg.BrainProvider != "ollama" || !strings.Contains(m.message, "optional") {
		t.Fatalf("selection=%s message=%s", m.cfg.BrainProvider, m.message)
	}
	saved, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if saved.BrainProvider != "ollama" {
		t.Fatal("missing Obey replaced saved provider")
	}
}
