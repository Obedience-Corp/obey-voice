package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Starting a session must say so. The build runs off the update loop for
// seconds to minutes; the screen used to render an empty conversation with
// "Preparing..." in the header corner, which reads as a hang.
func TestStartPipelineShowsTheStartingScreen(t *testing.T) {
	app := wiredApp(func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) {
		return fakeRuntime(), nil
	})
	model, cmd := app.Update(startPipelineMsg{personaID: "uncle-fu", personaName: "Uncle Fu"})
	app = model.(App)

	if !app.conversation.startingUp() {
		t.Fatal("startPipelineMsg did not enter the starting state")
	}
	if cmd == nil {
		t.Fatal("no command returned; the spinner would never tick")
	}
	view := stripANSI(app.conversation.View())
	if !strings.Contains(view, "Starting Uncle Fu") {
		t.Fatalf("starting screen does not name the session:\n%s", view)
	}
	if !strings.Contains(view, "Preparing…") {
		t.Fatalf("starting screen shows no stage:\n%s", view)
	}
	if !strings.Contains(view, "0:00") {
		t.Fatalf("starting screen shows no elapsed clock:\n%s", view)
	}
}

// The clock is the liveness proof: it must move even when a stage does not.
func TestStartingScreenReportsElapsedTime(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 80, 24
	m.beginStartup("Samantha")
	m.startup.since = time.Now().Add(-95 * time.Second)
	if got := stripANSI(m.View()); !strings.Contains(got, "1:35") {
		t.Fatalf("elapsed not rendered as 1:35:\n%s", got)
	}
}

func TestBuildStagesReachTheStartingScreen(t *testing.T) {
	app := wiredApp(func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) {
		return fakeRuntime(), nil
	})
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	for _, tc := range []struct {
		stage BuildStage
		want  string
	}{
		{BuildStage{Label: "Loading speech recognition"}, "Loading speech recognition…"},
		{BuildStage{Label: "Downloading kokoro-v1", Download: true, Pct: 42}, "Downloading kokoro-v1: 42%"},
		{BuildStage{Label: "Opening the microphone"}, "Opening the microphone…"},
	} {
		model, cmd := app.Update(buildStageMsg{stage: tc.stage})
		app = model.(App)
		if cmd == nil {
			t.Fatal("stage handler did not re-arm the progress feed")
		}
		if got := stripANSI(app.conversation.View()); !strings.Contains(got, tc.want) {
			t.Fatalf("stage %q not shown:\n%s", tc.want, got)
		}
	}

	// An empty stage never blanks the line that is already there.
	model, _ = app.Update(buildStageMsg{stage: BuildStage{Label: "   "}})
	app = model.(App)
	if !strings.Contains(stripANSI(app.conversation.View()), "Opening the microphone") {
		t.Fatal("an empty stage overwrote the last real one")
	}
}

// A download says the wait is once; a model load says it is per start.
func TestStartingScreenExplainsTheWait(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 100, 24
	m.beginStartup("Samantha")

	m.setBuildStage(BuildStage{Label: "Downloading parakeet", Download: true})
	if got := stripANSI(m.View()); !strings.Contains(got, "First run downloads") {
		t.Fatalf("download note missing:\n%s", got)
	}
	m.setBuildStage(BuildStage{Label: "Loading the voice"})
	if got := stripANSI(m.View()); !strings.Contains(got, "Models load at session start") {
		t.Fatalf("load note missing:\n%s", got)
	}
}

// Typing before the pipeline exists used to reach a nil runner.
func TestStartingScreenIgnoresInput(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 80, 24
	m.beginStartup("Samantha")
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("composer took input during startup: %q", got)
	}
	if !m.startingUp() {
		t.Fatal("a keystroke left the starting state")
	}
}

// The spinner ticks while starting and stops when the runtime lands.
func TestStartupTickStopsWhenReady(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 80, 24
	m.beginStartup("Samantha")

	before := m.startup.frame
	m, cmd := m.Update(startupTickMsg(time.Now()))
	if m.startup.frame == before {
		t.Fatal("tick did not advance the spinner")
	}
	if cmd == nil {
		t.Fatal("tick did not reschedule itself")
	}

	m.finishStartup()
	if m.startingUp() {
		t.Fatal("finishStartup left the screen starting")
	}
	m, cmd = m.Update(startupTickMsg(time.Now()))
	if cmd != nil {
		t.Fatal("tick kept running after the runtime was ready")
	}
}

// Reduced motion keeps the clock (information) and drops the spinner
// (decoration), on a slower tick.
func TestStartingScreenRespectsReducedMotion(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 80, 24
	m.reducedMotion = true
	m.beginStartup("Samantha")
	m.startup.frame = 3
	view := stripANSI(m.View())
	for _, glyph := range startupSpinner {
		if strings.Contains(view, glyph) {
			t.Fatalf("reduced motion still animates (%s):\n%s", glyph, view)
		}
	}
	if !strings.Contains(view, "0:00") {
		t.Fatalf("reduced motion dropped the elapsed clock:\n%s", view)
	}
}

// The runtime landing hands the screen back to the conversation proper.
func TestRuntimeReadyClearsTheStartingScreen(t *testing.T) {
	rt := fakeRuntime()
	app := wiredApp(func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) {
		return rt, nil
	})
	model, _ := app.Update(startPipelineMsg{personaName: "Uncle Fu"})
	app = model.(App)
	model, _ = app.Update(runtimeReadyMsg{rt: rt})
	app = model.(App)

	if app.conversation.startingUp() {
		t.Fatal("the starting screen outlived the build")
	}
	if strings.Contains(stripANSI(app.conversation.View()), "Starting Uncle Fu") {
		t.Fatal("starting screen still rendered after ready")
	}
}
