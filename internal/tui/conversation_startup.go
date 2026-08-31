package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

// The conversation screen while its runtime is being built.
//
// The build loads models and opens devices — seconds at best, much longer when
// a model is cold — and it runs off the update loop, so the screen is free to
// say so. It used to render the full conversation chrome with "Preparing..."
// in the header corner: an empty transcript, a composer that accepted text
// nothing would answer, and no sign the program was doing anything at all.

const (
	// startupTickInterval drives the spinner (~8 fps).
	startupTickInterval = 120 * time.Millisecond
	// startupReducedTickInterval still moves the clock without animating: the
	// elapsed time is information, not decoration, so reduced motion keeps it.
	startupReducedTickInterval = time.Second
)

// startupSpinner is the animated glyph; reduced motion gets a static one.
var startupSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// startupTickMsg advances the starting screen.
type startupTickMsg time.Time

func startupTickCmd(reduced bool) tea.Cmd {
	d := startupTickInterval
	if reduced {
		d = startupReducedTickInterval
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return startupTickMsg(t) })
}

// startupState is what the screen knows about a build in flight.
type startupState struct {
	active bool
	label  string
	stage  BuildStage
	since  time.Time
	frame  int
}

// beginStartup puts the screen into the waiting state. name is what the
// session is being started as, when the caller knows it — the runtime's own
// identity is not resolved until the build finishes.
func (m *conversationModel) beginStartup(name string) tea.Cmd {
	m.startup = startupState{
		active: true,
		label:  strings.TrimSpace(name),
		stage:  BuildStage{Label: "Preparing"},
		since:  time.Now(),
	}
	return startupTickCmd(m.reducedMotion)
}

func (m *conversationModel) setBuildStage(stage BuildStage) {
	if !m.startup.active || strings.TrimSpace(stage.Label) == "" {
		return
	}
	m.startup.stage = stage
}

// finishStartup returns the screen to the conversation proper.
func (m *conversationModel) finishStartup() {
	m.startup = startupState{}
}

func (m conversationModel) startingUp() bool { return m.startup.active }

// startupElapsed is the wall time this build has taken, floored to the second.
func (m conversationModel) startupElapsed() time.Duration {
	if m.startup.since.IsZero() {
		return 0
	}
	return time.Since(m.startup.since).Truncate(time.Second)
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// startupView is the whole screen while the runtime is being built.
func (m conversationModel) startupView() string {
	// The screen can render before the first WindowSizeMsg lands; 80 is the
	// same fallback the other screens use.
	width := m.width
	if width <= 0 {
		width = 80
	}
	glyph := "●"
	if !m.reducedMotion {
		glyph = startupSpinner[m.startup.frame%len(startupSpinner)]
	}
	name := m.startup.label
	if name == "" {
		name = m.agentName
	}
	title := "Starting a conversation"
	if name != "" {
		title = "Starting " + name
	}

	stage := m.startup.stage.Text()
	elapsed := formatElapsed(m.startupElapsed())
	// The stage and the clock share a line, the clock pinned right: a number
	// that moves every second is the clearest proof the program is alive.
	line := agentStyle.Render(glyph) + "  " + normalStyle.Render(stage)
	if pad := width - 2 - ansi.StringWidth(line) - ansi.StringWidth(elapsed) - 2; pad > 0 {
		line += strings.Repeat(" ", pad) + dimStyle.Render(elapsed)
	} else {
		line += "  " + dimStyle.Render(elapsed)
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + headerStyle.Render(title) + "\n\n")
	inner := max(width-2, 20)
	b.WriteString("  " + ansi.Truncate(line, inner, "…") + "\n\n")
	b.WriteString("  " + dimStyle.Render(ansi.Truncate(startupNote(m.startup.stage), inner, "…")) + "\n")
	b.WriteString("\n  " + dimStyle.Render("ctrl+c quits"))
	return b.String()
}

// startupNote explains the wait in the terms of the step being waited on.
func startupNote(stage BuildStage) string {
	if stage.Download {
		return "First run downloads the models once; later sessions skip this."
	}
	return "Models load at session start — the first one after a restart is the slow one."
}
