package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

func TestDecisionSettingsTerminalPersistenceAndIndependentPoints(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DecisionEnabled {
		t.Fatal("experimental feature defaulted on")
	}
	applied := make(chan *config.Config, 8)
	live := &pipeline.Pipeline{ConfigureDecisions: func(cfg *config.Config) { applied <- cfg }}
	app := App{cfg: cfg, runtime: &ConversationRuntime{Pipeline: live}}
	opened, _ := app.onSwitchScreen(switchScreenMsg(screenSettings))
	m := opened.(App).settings
	m.section, m.width, m.height = sectionBrain, 120, 40
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
	key := func(msg tea.KeyMsg) workerTerminalState {
		p.Send(msg)
		select {
		case state := <-h.states:
			return state
		case <-ctx.Done():
			t.Fatal("key timed out")
			return workerTerminalState{}
		}
	}
	navigate := func(want string) {
		state := key(tea.KeyMsg{Type: tea.KeyHome})
		for i := 0; i < 100 && state.key != want; i++ {
			state = key(tea.KeyMsg{Type: tea.KeyDown})
		}
		if state.key != want {
			t.Fatalf("cannot reach %s", want)
		}
	}
	change := func(name string) {
		navigate(name)
		key(tea.KeyMsg{Type: tea.KeyEnter})
		select {
		case <-applied:
		case <-ctx.Done():
			t.Fatal("saved settings did not reach runtime")
		}
	}
	change("decision_enabled")
	change("decision_voice_work")
	loaded, err := config.LoadWithoutHooks()
	if err != nil || !loaded.DecisionEnabled || loaded.DecisionVoiceWork || !loaded.DecisionTextWork || loaded.DecisionProvider != "local" {
		t.Fatalf("saved settings=%+v err=%v", loaded, err)
	}
	navigate("decision_budget_ms")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	key(tea.KeyMsg{Type: tea.KeyCtrlA})
	key(tea.KeyMsg{Type: tea.KeyCtrlK})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("99999")})
	state := key(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(state.message, "25–3000") {
		t.Fatalf("invalid deadline message=%s", state.message)
	}
	loaded, err = config.LoadWithoutHooks()
	if err != nil || loaded.DecisionBudgetMS != 750 {
		t.Fatal("invalid deadline persisted")
	}
	change("decision_enabled")
	loaded, err = config.LoadWithoutHooks()
	if err != nil || loaded.DecisionEnabled || loaded.DecisionVoiceWork || !loaded.DecisionTextWork {
		t.Fatal("disable changed independent point preferences")
	}
}

func TestDecisionSaveFailureDoesNotChangeRuntime(t *testing.T) {
	cfg := &config.Config{}
	m := newSettings(cfg, nil)
	m.saveConfig = func(string, any) error { return errors.New("fixture write failure") }
	m.onDecisionSettings = func(*config.Config) { t.Error("failed save reached runtime") }
	m.saveDecision("decision_enabled", true)
	if cfg.DecisionEnabled {
		t.Fatal("failed save changed config")
	}
}
