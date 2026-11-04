package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
)

// The conversation screen while its runtime is being built.
//
// The build loads models and opens devices — seconds at best, much longer when
// a model is cold — and it runs off the update loop, so the screen is free to
// say so. It used to render the full conversation chrome with "Preparing..."
// in the header corner: an empty transcript, a composer that accepted text
// nothing would answer, and no sign the program was doing anything at all.
//
// What it shows now, in the order it matters: the presence mark turning (this
// program is alive), the step being waited on with the steps already done, a
// bar, and an elapsed clock. The clock is the one that survives every layout —
// when a single stage takes thirty seconds it is the only proof of progress.

const (
	// startupTickInterval drives the orb and the sweep (~12 fps).
	startupTickInterval = 80 * time.Millisecond
	// startupReducedTickInterval still moves the clock without animating: the
	// elapsed time is information, not decoration, so reduced motion keeps it.
	startupReducedTickInterval = time.Second
	// startupBlockWidth is the width of the text block under the orb.
	startupBlockWidth = 52
	// startupHistoryMax caps the completed-step list so the block cannot grow
	// past the screen on a long cold start.
	startupHistoryMax = 4
)

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
	label  string // meeting title; conversation startup uses the product title
	stage  BuildStage
	// done is the steps already finished, oldest first.
	done  []string
	since time.Time
	frame int
}

// begin puts the state into the waiting state. Conversation startup uses the
// product identity; meeting startup uses the label as its recording title.
func (s *startupState) begin(label string) {
	// The opening stage carries no label: it is a placeholder for the first
	// real step, and retiring it would put "✓ Preparing" in a list of steps
	// that actually happened.
	*s = startupState{active: true, label: strings.TrimSpace(label), since: time.Now()}
}

// advance moves to the next step, retiring the current one to the done list. A
// download reports many times under one label; that is one step in the list,
// not one per percent.
func (s *startupState) advance(stage BuildStage) {
	if !s.active || strings.TrimSpace(stage.Label) == "" {
		return
	}
	if stage.Label == s.stage.Label {
		s.stage = stage
		return
	}
	if prev := strings.TrimSpace(s.stage.Label); prev != "" {
		s.done = append(s.done, prev)
	}
	s.stage = stage
}

// tick advances the animation, reporting whether the loop should continue.
func (s *startupState) tick() bool {
	if !s.active {
		return false
	}
	s.frame++
	return true
}

// beginStartup puts the screen into the waiting state. name is retained at the
// call boundary because the runtime builder still uses it for persona binding;
// the startup screen itself is branded as Obey Voice.
func (m *conversationModel) beginStartup(name string) tea.Cmd {
	m.startup.begin(name)
	return startupTickCmd(m.reducedMotion)
}

func (m *conversationModel) setBuildStage(stage BuildStage) { m.startup.advance(stage) }

// finishStartup returns the screen to the conversation proper.
func (m *conversationModel) finishStartup() {
	m.startup = startupState{}
}

func (m conversationModel) startingUp() bool { return m.startup.active }

// elapsed is the wall time this build has taken, floored to the second.
func (s startupState) elapsed() time.Duration {
	if s.since.IsZero() {
		return 0
	}
	return time.Since(s.since).Truncate(time.Second)
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// orbStyles paints the presence mark: brand cyan rings, a white middle C, and
// a dim core. The voice meter's palette is cyan throughout, which would lose
// the middle ring.
func orbStyles() anim.Styles {
	return anim.Styles{
		Accent: lipgloss.NewStyle().Foreground(colorAccent),
		Label:  lipgloss.NewStyle().Foreground(colorNormal),
		Muted:  lipgloss.NewStyle().Foreground(colorDim),
	}
}

// startupLayout sizes the screen to the terminal: the orb is the first thing
// to shrink and the first thing to go, the clock the last.
func startupLayout(width, height int) (orbRows, history int) {
	switch {
	case height >= 30 && width >= 60:
		return 11, startupHistoryMax
	case height >= 24 && width >= 44:
		return 9, 3
	case height >= 18 && width >= 34:
		return 7, 2
	case height >= 14:
		return 5, 1
	default:
		return 0, 0
	}
}

// startupView is the whole screen while the runtime is being built.
func (m conversationModel) startupView() string {
	return m.startup.view(m.width, m.height, m.startupTitle(), m.reducedMotion)
}

func (m conversationModel) startupTitle() string {
	return "Starting Obey Voice"
}

// view renders the whole waiting screen. It belongs to the state rather than
// to one model: the conversation and the meeting recorder wait on the same
// kind of build and have the same nothing else to show meanwhile.
func (s startupState) view(width, height int, title string, reduced bool) string {
	// The screen can render before the first WindowSizeMsg lands; 80x24 is the
	// same fallback the other screens use.
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	orbRows, history := startupLayout(width, height)
	block := min(startupBlockWidth, max(width-4, 24))

	var lines []string
	if orbRows > 0 {
		lines = append(lines, centerBlock(anim.Orb(s.frame, orbRows, orbStyles(), reduced), width)...)
		lines = append(lines, "")
	}
	lines = append(lines, centerLine(headerStyle.Render(title), width), "")
	lines = append(lines, centerBlock(s.steps(block, history, reduced), width)...)
	lines = append(lines, "", centerLine(dimStyle.Render(startupNote(s.stage)), width))
	lines = append(lines, "", centerLine(dimStyle.Render("ctrl+c quits"), width))

	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return "\n" + strings.Join(lines, "\n")
}

// steps is the checklist block: finished steps, then the one in flight with
// the clock, then its bar.
func (s startupState) steps(width, history int, reduced bool) []string {
	styles := orbStyles()
	var lines []string

	done := s.done
	if history <= 0 {
		done = nil
	} else if len(done) > history {
		done = done[len(done)-history:]
	}
	for _, step := range done {
		lines = append(lines, statusStyle.Render("✓ ")+dimStyle.Render(step))
	}

	// Current step, with the clock pushed to the right edge of the block. No
	// spinner here: the orb above is the motion, and the bar below is the
	// second one — a third would be noise.
	step := s.stage.Label
	if strings.TrimSpace(step) == "" {
		step = "Preparing"
	}
	left := selectedStyle.Render("▸ ") + normalStyle.Render(step+"…")
	elapsed := dimStyle.Render(formatElapsed(s.elapsed()))
	if pad := width - ansi.StringWidth(left) - ansi.StringWidth(elapsed); pad > 0 {
		left += strings.Repeat(" ", pad) + elapsed
	} else {
		left += " " + elapsed
	}
	lines = append(lines, left)

	if s.stage.Download {
		lines = append(lines, "  "+anim.Bar(s.stage.Pct, width-2, styles))
	} else {
		lines = append(lines, "  "+anim.Sweep(s.frame, width-2, styles, reduced))
	}
	return lines
}

// startupNote explains the wait in the terms of the step being waited on.
func startupNote(stage BuildStage) string {
	if stage.Download {
		return "First run downloads the models once; later sessions skip this."
	}
	return "Models load at session start — the first one after a restart is the slow one."
}

// centerLine centres one rendered line in width columns.
func centerLine(line string, width int) string {
	pad := (width - ansi.StringWidth(line)) / 2
	if pad <= 0 {
		return line
	}
	return strings.Repeat(" ", pad) + line
}

// centerBlock centres a group of lines as one block, so their left edges stay
// aligned with each other rather than each line floating on its own.
func centerBlock(lines []string, width int) []string {
	widest := 0
	for _, line := range lines {
		widest = max(widest, ansi.StringWidth(line))
	}
	pad := (width - widest) / 2
	if pad <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	prefix := strings.Repeat(" ", pad)
	for i, line := range lines {
		out[i] = prefix + line
	}
	return out
}
