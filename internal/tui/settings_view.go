package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

// The screen is a sidebar of sections beside the section's lines. Every
// section is on the sidebar, numbered, so the map of the screen is always in
// view and any section is one keypress away. Lists draw whole, with ✓ on the
// current item: seeing the options is what a settings screen is for.
var sectionNames = [settingsSectionCount]string{
	sectionBrain:    "Brain",
	sectionWorker:   "Worker",
	sectionTools:    "Tools",
	sectionVoice:    "Voice",
	sectionQwen:     "Qwen",
	sectionSTT:      "STT",
	sectionSpeakers: "Speakers",
	sectionAudio:    "Audio",
	sectionMeeting:  "Meeting",
}

// groupStyle heads a list inside a section.
var groupStyle = lipgloss.NewStyle().Foreground(colorNormal).Bold(true)

func (m settingsModel) View() string {
	width := m.renderWidth()
	compact := m.isCompact()
	rows := m.visibleRows()

	var parts []string
	parts = append(parts, headerStyle.Render("  Settings"))
	if !compact {
		// Full terminal width so the chrome tracks resizes/splits.
		parts = append(parts, dimStyle.Render(strings.Repeat("─", max(width, 1))))
	}
	side := m.sidebarWidth()
	if side == 0 {
		parts = append(parts, ansi.Truncate(m.sectionStrip(), width, "…"))
	}

	content := m.contentLines()
	if len(content) > rows {
		content = content[:rows]
	}
	for len(content) < rows {
		content = append(content, "")
	}
	if side > 0 {
		bar := m.sidebarLines(rows)
		for i := range content {
			content[i] = padRight(bar[i], side) + dimStyle.Render("│ ") + content[i]
		}
	}
	for i := range content {
		content[i] = ansi.Truncate(content[i], width, "…")
	}
	parts = append(parts, content...)

	help := m.footerHelp()
	if compact {
		footer := dimStyle.Render(help)
		if m.message != "" {
			footer = statusStyle.Render("  " + m.message)
		}
		parts = append(parts, ansi.Truncate(footer, width, "…"))
	} else {
		switch {
		case m.message != "":
			parts = append(parts, ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		default:
			parts = append(parts, ansi.Truncate(m.detailLine(), width, "…"))
		}
		parts = append(parts, dimStyle.Render(ansi.Truncate(help, width, "…")))
	}

	return strings.Join(parts, "\n")
}

func (m settingsModel) isCompact() bool {
	return m.height > 0 && m.height < 12
}

// sidebarWidth is the sidebar's column, or 0 on a terminal too narrow to
// give the section lines room beside it.
func (m settingsModel) sidebarWidth() int {
	if m.renderWidth() < 60 {
		return 0
	}
	longest := 0
	for _, name := range sectionNames {
		longest = max(longest, lipgloss.Width(name))
	}
	digits := len(fmt.Sprint(int(settingsSectionCount)))
	return 2 + digits + 1 + longest + 2
}

// contentWidth is the room a section line has.
func (m settingsModel) contentWidth() int {
	side := m.sidebarWidth()
	if side == 0 {
		return m.renderWidth()
	}
	return max(m.renderWidth()-side-2, 20)
}

// sidebarLines is the section list, one per content row, windowed around the
// current section when the terminal is shorter than the list.
func (m settingsModel) sidebarLines(rows int) []string {
	n := int(settingsSectionCount)
	start := 0
	if n > rows {
		start = min(max(int(m.section)-rows/2, 0), n-rows)
	}
	out := make([]string, rows)
	for i := range out {
		s := start + i
		if s >= n {
			continue
		}
		label := fmt.Sprintf("%d %s", s+1, sectionNames[s])
		if settingsSection(s) == m.section {
			out[i] = "  " + selectedStyle.Render(label)
		} else {
			out[i] = "  " + dimStyle.Render(label)
		}
	}
	return out
}

// sectionStrip stands in for the sidebar on a narrow terminal.
func (m settingsModel) sectionStrip() string {
	n := int(settingsSectionCount)
	return "  " + dimStyle.Render("‹ ") + selectedStyle.Render(sectionNames[m.section]) +
		dimStyle.Render(fmt.Sprintf(" ›  %d/%d", int(m.section)+1, n))
}

func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}

// contentLines is the section, or whatever has taken it over: a voice
// registration, an armed removal, the value editor above the lines.
func (m settingsModel) contentLines() []string {
	var lines []string
	switch {
	case m.enroll.active:
		lines = m.enrollOverlayLines()
	case m.confirmRemoveVoice != "":
		lines = m.confirmRemoveVoiceLines()
	default:
		lines = m.lineViews()
	}
	if m.edit.kind != editNone {
		lines = append([]string{m.editorLine(), ""}, lines...)
	}
	return lines
}

// lineViews draws the visible window of the section's lines.
func (m settingsModel) lineViews() []string {
	rows := m.sectionRows()
	lines := linesFor(rows)
	if len(lines) == 0 {
		return nil
	}
	nameWidth := 0
	for _, row := range rows {
		if !row.expands() {
			nameWidth = max(nameWidth, lipgloss.Width(row.name))
		}
	}
	nameWidth = min(max(nameWidth, 12), 30) + 2
	start, end := m.visibleRange(len(lines))
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		l := lines[i]
		switch {
		case l.blank:
			out = append(out, "")
		case l.header:
			out = append(out, m.headerLine(rows[l.row]))
		case l.option >= 0:
			out = append(out, m.optionLine(rows[l.row], l.option, i == m.cursor))
		default:
			out = append(out, m.rowLine(rows[l.row], i == m.cursor, nameWidth))
		}
	}
	return out
}

// headerLine heads a list: its name, its current value (the ✓ may be
// scrolled away on a long list), and the row's detail.
func (m settingsModel) headerLine(row settingRow) string {
	line := "  " + groupStyle.Render(row.name)
	if row.value != "" {
		line += dimStyle.Render(" · " + row.value)
	}
	if row.detail != "" {
		line += "  " + dimStyle.Render(row.detail)
	}
	return ansi.Truncate(line, m.contentWidth(), "…")
}

// optionLine is one option of a list: ✓ on the current one, dim when muted.
func (m settingsModel) optionLine(row settingRow, idx int, focused bool) string {
	cursor, style := "  ", normalStyle
	if idx < len(row.muted) && row.muted[idx] {
		style = dimStyle
	}
	if focused {
		cursor, style = "▸ ", selectedStyle
	}
	mark := "  "
	if idx == row.current {
		mark = statusStyle.Render("✓ ")
	}
	return ansi.Truncate("  "+cursor+mark+style.Render(row.options[idx]), m.contentWidth(), "…")
}

// rowLine is a row that is its own value: name, then the value drawn so the
// verb is legible before any key is pressed.
func (m settingsModel) rowLine(row settingRow, focused bool, nameWidth int) string {
	cursor := "  "
	nameStyle := normalStyle
	if row.kind == rowInfo || row.disabled {
		nameStyle = dimStyle
	}
	if focused {
		cursor = "▸ "
		nameStyle = selectedStyle
	}
	name := row.name
	if lipgloss.Width(name) > nameWidth-1 {
		name = ansi.Truncate(name, nameWidth-2, "…")
	}
	pad := max(nameWidth-lipgloss.Width(name), 1)
	line := "  " + cursor + nameStyle.Render(name) + strings.Repeat(" ", pad) + m.rowValue(row)
	if row.detail != "" && !row.disabled {
		line += "  " + dimStyle.Render(row.detail)
	}
	return ansi.Truncate(line, m.contentWidth(), "…")
}

// rowValue is the value column: [current] among its few options for a
// choice, ON/OFF for a toggle, a level with its keys for a slider, an
// ellipsis where enter opens something.
func (m settingsModel) rowValue(row settingRow) string {
	if row.disabled {
		return dimStyle.Render("— " + row.reason)
	}
	switch row.kind {
	case rowChoice:
		parts := make([]string, 0, len(row.options)+1)
		if row.current < 0 && row.value != "" {
			parts = append(parts, normalStyle.Render(row.value)+dimStyle.Render(" ·"))
		}
		for i, option := range row.options {
			if i == row.current {
				parts = append(parts, statusStyle.Render("["+option+"]"))
			} else {
				parts = append(parts, dimStyle.Render(" "+option+" "))
			}
		}
		return strings.Join(parts, " ")
	case rowPick, rowMenu:
		return dimStyle.Render("nothing to choose from")
	case rowToggle:
		if row.value == "ON" {
			return statusStyle.Render("ON")
		}
		return dimStyle.Render("OFF")
	case rowSlider:
		return normalStyle.Render(row.value) + dimStyle.Render("  - / +")
	case rowText:
		return normalStyle.Render(row.value)
	case rowAction, rowInfo:
		return dimStyle.Render(row.value)
	}
	return normalStyle.Render(row.value)
}

// detailLine is what the focused line does and when a change applies — facts
// that stay on screen, unlike a status message.
func (m settingsModel) detailLine() string {
	row, ok := m.focusedRow()
	if !ok {
		return " "
	}
	if row.disabled {
		return "  " + warningStyle.Render(row.reason)
	}
	var parts []string
	if row.help != "" {
		parts = append(parts, row.help)
	}
	if row.applies != "" {
		parts = append(parts, row.applies)
	}
	if len(parts) == 0 {
		return " "
	}
	return "  " + dimStyle.Render(strings.Join(parts, " · "))
}

// footerHelp names the keys that apply right now.
func (m settingsModel) footerHelp() string {
	switch {
	case m.enroll.active:
		return "  recording your voice • esc cancel"
	case m.confirmRemoveVoice != "":
		return "  y remove • any other key keeps it"
	case m.edit.kind != editNone:
		return "  type a value • enter save • esc cancel"
	}
	parts := []string{"↑/↓ move", "←/→ section"}
	if row, line, ok := m.focused(); ok && !row.disabled {
		switch {
		case line.option >= 0:
			verb := "choose"
			if row.kind == rowMenu && row.verb != "" {
				verb = row.verb
			}
			parts = append(parts, "enter "+verb)
			if row.preview != nil {
				parts = append(parts, "p preview")
			}
		default:
			switch row.kind {
			case rowChoice:
				parts = append(parts, "enter next", "+/- step")
			case rowToggle:
				parts = append(parts, "enter or space toggle")
			case rowSlider:
				parts = append(parts, "+/- adjust")
			case rowText:
				parts = append(parts, "enter edit")
			default:
				verb := row.verb
				if verb == "" {
					verb = "select"
				}
				parts = append(parts, "enter "+verb)
			}
		}
	}
	parts = append(parts, "esc back")
	return "  " + strings.Join(parts, " • ")
}

func (m settingsModel) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}
