package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
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
	live := &pipeline.Pipeline{ConfigureDecisions: func(cfg *config.Config) { applied <- cfg }, DecisionStatus: func() decision.RuntimeStatus { return decision.RuntimeStatus{Phase: "ready"} }, TestDecisions: func(context.Context) (decision.SmokeResult, error) {
		return decision.SmokeResult{Runtime: "fixture · cpu", Elapsed: 15 * time.Millisecond, Budget: 750 * time.Millisecond}, nil
	}}
	app := App{cfg: cfg, runtime: &ConversationRuntime{Pipeline: live}}
	opened, _ := app.onSwitchScreen(switchScreenMsg(screenSettings))
	m := opened.(App).settings
	m.section, m.width, m.height = sectionBrain, 120, 40
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 1), decisionResults: make(chan string, 1)}
	// The expanded scenario renders model/device choices and the test result in
	// addition to the original switches. Bound individual acknowledgments below;
	// a single five-second budget for every rendered frame is too tight under
	// the full Linux race suite.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case state := <-h.states:
			return state
		case <-ctx.Done():
			t.Fatalf("terminal scenario deadline during key %s", msg.String())
			return workerTerminalState{}
		case <-timer.C:
			t.Fatalf("terminal did not acknowledge key %s", msg.String())
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
		key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
		select {
		case <-applied:
		case <-ctx.Done():
			t.Fatal("saved settings did not reach runtime")
		}
	}
	change("decision_enabled")
	change("decision_voice_work")
	change("decision_local_model")
	change("decision_device")
	loaded, err := config.LoadWithoutHooks()
	if err != nil || !loaded.DecisionEnabled || loaded.DecisionVoiceWork || !loaded.DecisionTextWork || loaded.DecisionProvider != "local" || loaded.DecisionLocalModel != "0.8b" || loaded.DecisionDevice != "cpu" {
		t.Fatalf("saved settings=%+v err=%v", loaded, err)
	}
	navigate("decision_test")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case message := <-h.decisionResults:
		if !strings.Contains(message, "synthetic choice passed") {
			t.Fatal(message)
		}
	case <-ctx.Done():
		t.Fatal("terminal test result never arrived")
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

func TestDecisionTestResultsDoNotSurviveSelectionOrScreenChange(t *testing.T) {
	cfg := &config.Config{DecisionEnabled: true, DecisionVoiceWork: true, DecisionProvider: "local", DecisionRuntime: "managed", DecisionLocalModel: "2b", DecisionDevice: "cpu"}
	m := newSettings(cfg, nil)
	m.saveConfig = func(string, any) error { return nil }
	m.decisionStatus = func() decision.RuntimeStatus { return decision.RuntimeStatus{Phase: "ready"} }
	calls := 0
	m.testDecisions = func(context.Context) (decision.SmokeResult, error) {
		calls++
		return decision.SmokeResult{Runtime: "fixture · cpu", Elapsed: 10 * time.Millisecond, Budget: 750 * time.Millisecond}, nil
	}
	var cmd tea.Cmd
	for _, row := range m.decisionRows() {
		if row.key == "decision_test" {
			if row.disabled {
				t.Fatal("ready action disabled")
			}
			cmd = row.run(&m)
		}
	}
	if cmd == nil || !m.decisionTestRunning {
		t.Fatal("test did not start")
	}
	msg := cmd()
	if calls != 1 {
		t.Fatal("synthetic test callback missing")
	}
	result, _ := m.Update(msg)
	if !strings.Contains(result.message, "synthetic choice passed") || result.decisionTestRunning {
		t.Fatal("test result not displayed")
	}
	m.saveDecision("decision_local_model", "d1-3b")
	result, _ = m.Update(msg)
	if strings.Contains(result.message, "synthetic choice passed") {
		t.Fatal("old model result applied")
	}
	fresh := newSettings(cfg, nil)
	result, _ = fresh.Update(msg)
	if strings.Contains(result.message, "synthetic choice passed") {
		t.Fatal("old screen result applied")
	}
}
