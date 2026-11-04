package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
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
	if !strings.Contains(view, "Starting Obey Voice") {
		t.Fatalf("starting screen does not show the product title:\n%s", view)
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
		want  []string
	}{
		{BuildStage{Label: "Loading speech recognition"}, []string{"▸ Loading speech recognition…"}},
		// A download names the asset on the step line and puts the number on
		// the bar, where a percentage means something.
		{BuildStage{Label: "Downloading kokoro-v1", Download: true, Pct: 42},
			[]string{"▸ Downloading kokoro-v1…", "42%", "✓ Loading speech recognition"}},
		{BuildStage{Label: "Opening the microphone"}, []string{"▸ Opening the microphone…", "✓ Downloading kokoro-v1"}},
	} {
		model, cmd := app.Update(buildStageMsg{stage: tc.stage})
		app = model.(App)
		if cmd == nil {
			t.Fatal("stage handler did not re-arm the progress feed")
		}
		got := stripANSI(app.conversation.View())
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Fatalf("stage screen missing %q:\n%s", want, got)
			}
		}
	}

	// A download reports many times under one label: that is one step in the
	// list, not one per percent.
	for _, pct := range []float64{10, 55, 99} {
		model, _ := app.Update(buildStageMsg{stage: BuildStage{Label: "Downloading parakeet", Download: true, Pct: pct}})
		app = model.(App)
	}
	if got := strings.Count(stripANSI(app.conversation.View()), "Downloading parakeet"); got != 1 {
		t.Fatalf("one download appears %d times in the step list", got)
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

// Reduced motion keeps the clock and the layout (information) and drops the
// motion: the orb stops turning and the indeterminate bar stops travelling.
func TestStartingScreenRespectsReducedMotion(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 80, 24
	m.reducedMotion = true
	m.beginStartup("Samantha")

	first := stripANSI(m.View())
	m.startup.frame = 17
	if second := stripANSI(m.View()); second != first {
		t.Fatalf("reduced motion still animates between frames:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(first, "0:00") {
		t.Fatalf("reduced motion dropped the elapsed clock:\n%s", first)
	}
	if !strings.Contains(first, "Starting Obey Voice") {
		t.Fatalf("reduced motion dropped the screen:\n%s", first)
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
	if strings.Contains(stripANSI(app.conversation.View()), "Starting Obey Voice") {
		t.Fatal("starting screen still rendered after ready")
	}
}

// The orb is the first thing to shrink and the first thing to go; the clock
// and the current step survive every layout.
func TestStartingScreenFitsSmallTerminals(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 40}, {80, 24}, {70, 20}, {60, 16}, {40, 12}, {30, 10}} {
		m := newConversation("Samantha")
		m.width, m.height = size.w, size.h
		m.beginStartup("Samantha")
		m.setBuildStage(BuildStage{Label: "Opening the microphone"})

		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: screen is %d rows, exceeds the %d available", size.w, size.h, len(lines), size.h)
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got > size.w {
				t.Errorf("%dx%d: line %q is %d cells wide", size.w, size.h, stripANSI(line), got)
			}
		}
		plain := stripANSI(view)
		if !strings.Contains(plain, "Opening the microphone") {
			t.Errorf("%dx%d: current step dropped:\n%s", size.w, size.h, plain)
		}
		if !strings.Contains(plain, "0:00") {
			t.Errorf("%dx%d: elapsed clock dropped:\n%s", size.w, size.h, plain)
		}
	}
}

// A download gets a bar that means something; everything else gets one that
// does not pretend to.
func TestStartingScreenBarMatchesTheStage(t *testing.T) {
	m := newConversation("Samantha")
	m.width, m.height = 90, 30
	m.beginStartup("Samantha")

	m.setBuildStage(BuildStage{Label: "Loading the voice"})
	if got := stripANSI(m.View()); strings.Contains(got, "%") {
		t.Fatalf("a model load claimed a percentage:\n%s", got)
	}
	m.setBuildStage(BuildStage{Label: "Downloading kokoro", Download: true, Pct: 73})
	if got := stripANSI(m.View()); !strings.Contains(got, "73%") {
		t.Fatalf("download bar does not show its percentage:\n%s", got)
	}
}
