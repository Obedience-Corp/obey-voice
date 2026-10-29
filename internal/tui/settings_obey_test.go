package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestObeySettingsTerminalPersistence(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BrainProvider = "obey"
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionBrain, 110, 35
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
	for _, field := range []struct{ key, value string }{{"obey_provider", "fixture-provider"}, {"obey_model", "fixture-model"}, {"obey_camp", "fixture-camp"}, {"obey_socket", "/tmp/fixture.sock"}, {"obey_tool_policy", "daemon"}} {
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
	if loaded.ObeyProvider != "fixture-provider" || loaded.ObeyModel != "fixture-model" || loaded.ObeyCamp != "fixture-camp" || loaded.ObeySocket != "/tmp/fixture.sock" || loaded.ObeyToolPolicy != "daemon" {
		t.Fatal("terminal edits were not persisted")
	}
}
