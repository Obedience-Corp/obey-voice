package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// A line is what the cursor rests on. A list row (pick, menu) contributes a
// heading, one line per option and a blank after it; every other row is one
// line. Headings and blanks are skipped by the cursor, so ↑/↓ only ever land
// on something enter can act on.
type settingLine struct {
	row int
	// option indexes the row's options on a list, or is -1 for the row itself.
	option int
	header bool
	blank  bool
}

func (l settingLine) focusable() bool { return !l.header && !l.blank }

// expands reports whether a row draws its options under a heading. A list
// with nothing in it, or one that is disabled, draws as a single line that
// says why.
func (r settingRow) expands() bool {
	return (r.kind == rowPick || r.kind == rowMenu) && !r.disabled && len(r.options) > 0
}

func linesFor(rows []settingRow) []settingLine {
	var lines []settingLine
	blank := func(i int) {
		if n := len(lines); n > 0 && !lines[n-1].blank {
			lines = append(lines, settingLine{row: i, option: -1, blank: true})
		}
	}
	for i, row := range rows {
		if !row.expands() {
			lines = append(lines, settingLine{row: i, option: -1})
			continue
		}
		blank(i)
		lines = append(lines, settingLine{row: i, option: -1, header: true})
		for j := range row.options {
			lines = append(lines, settingLine{row: i, option: j})
		}
		blank(i)
	}
	for n := len(lines); n > 0 && lines[n-1].blank; n = len(lines) {
		lines = lines[:n-1]
	}
	return lines
}

func (m settingsModel) sectionLines() []settingLine { return linesFor(m.sectionRows()) }

// focused is the row under the cursor and, on a list, which of its options.
func (m settingsModel) focused() (settingRow, settingLine, bool) {
	rows := m.sectionRows()
	lines := linesFor(rows)
	if m.cursor < 0 || m.cursor >= len(lines) || !lines[m.cursor].focusable() {
		return settingRow{}, settingLine{}, false
	}
	l := lines[m.cursor]
	return rows[l.row], l, true
}

func (m settingsModel) focusedRow() (settingRow, bool) {
	row, _, ok := m.focused()
	return row, ok
}

// moveCursor moves |delta| focusable lines in delta's direction and stops at
// either end.
func (m *settingsModel) moveCursor(delta int) {
	lines := m.sectionLines()
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	at := m.cursor
	for ; delta > 0; delta-- {
		next := at + step
		for next >= 0 && next < len(lines) && !lines[next].focusable() {
			next += step
		}
		if next < 0 || next >= len(lines) {
			break
		}
		at = next
	}
	m.cursor = at
	m.clampCursor()
}

// clampCursor keeps the cursor on a focusable line after the section changes
// shape (a place switch hides the strictness row; a removal drops a guest;
// a new engine brings a different voice list).
func (m *settingsModel) clampCursor() {
	lines := m.sectionLines()
	if len(lines) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = min(max(m.cursor, 0), len(lines)-1)
	for i := m.cursor; i < len(lines); i++ {
		if lines[i].focusable() {
			m.cursor = i
			return
		}
	}
	for i := m.cursor; i >= 0; i-- {
		if lines[i].focusable() {
			m.cursor = i
			return
		}
	}
}

// homeCursor is where a section opens the first time. A section that leads
// with a list opens on that list's current option, so the value is marked
// and on screen even when the list is long; anything else opens at the top.
func (m settingsModel) homeCursor() int {
	rows := m.sectionRows()
	lines := linesFor(rows)
	for i, l := range lines {
		if !l.focusable() {
			continue
		}
		if l.option < 0 {
			return i
		}
		for j := i; j < len(lines) && lines[j].row == l.row; j++ {
			if lines[j].option == rows[l.row].current {
				return j
			}
		}
		return i
	}
	return 0
}

// ── the verbs ────────────────────────────────────────────────────────

// activateRow is enter: choose the option under the cursor, move a choice
// row on, flip a toggle, edit a value, run an action. One key, and the line
// under the cursor said what it would do.
func (m *settingsModel) activateRow() tea.Cmd {
	row, line, ok := m.focused()
	if !ok {
		return nil
	}
	if row.disabled {
		if row.reason != "" {
			m.message = row.reason
		}
		return nil
	}
	if line.option >= 0 {
		if row.set == nil {
			return nil
		}
		return row.set(m, line.option)
	}
	switch row.kind {
	case rowChoice:
		return m.stepChoice(row, 1)
	case rowPick, rowMenu:
		if row.reason != "" {
			m.message = row.reason
		} else {
			m.message = "Nothing to choose from"
		}
		return nil
	default:
		if row.run != nil {
			return row.run(m)
		}
	}
	return nil
}

// stepChoice moves a choice row delta options along, wrapping. A choice row
// has a few options and no cost to any of them, so moving is choosing, as on
// a toggle.
func (m *settingsModel) stepChoice(row settingRow, delta int) tea.Cmd {
	n := len(row.options)
	if n == 0 || row.set == nil {
		return nil
	}
	next := 0
	if delta < 0 {
		next = n - 1
	}
	if row.current >= 0 {
		next = ((row.current+delta)%n + n) % n
	}
	return row.set(m, next)
}

// toggleRow is space: a toggle flips, a choice row moves on. Anything else
// ignores it, so a stray space is safe.
func (m *settingsModel) toggleRow() tea.Cmd {
	row, line, ok := m.focused()
	if !ok || row.disabled || line.option >= 0 {
		return nil
	}
	switch row.kind {
	case rowToggle:
		if row.run != nil {
			return row.run(m)
		}
	case rowChoice:
		return m.stepChoice(row, 1)
	}
	return nil
}

// adjustRow is + and -: nudge a slider, step a choice row. It never touches
// a list — an option is chosen, not stepped to — and never a section.
func (m *settingsModel) adjustRow(delta int) tea.Cmd {
	row, line, ok := m.focused()
	if !ok || row.disabled || line.option >= 0 {
		return nil
	}
	switch row.kind {
	case rowSlider:
		if row.nudge != nil {
			return row.nudge(m, delta)
		}
	case rowChoice:
		return m.stepChoice(row, delta)
	}
	return nil
}

// previewRow is p: play the option under the cursor without choosing it, or
// the row's current value when the cursor is not on an option.
func (m *settingsModel) previewRow() tea.Cmd {
	row, line, ok := m.focused()
	if !ok || row.disabled || row.preview == nil {
		return nil
	}
	idx := line.option
	if idx < 0 {
		idx = row.current
	}
	if idx < 0 {
		return nil
	}
	return row.preview(m, idx)
}

// switchSettingsSection moves delta sections, wrapping.
func (m *settingsModel) switchSettingsSection(delta int) tea.Cmd {
	n := int(settingsSectionCount)
	return m.gotoSection(settingsSection((int(m.section) + delta%n + n) % n))
}

// gotoSection leaves the cursor where it was in the section it leaves and
// returns to where it was in the one it enters; a section entered for the
// first time opens at its home line.
func (m *settingsModel) gotoSection(s settingsSection) tea.Cmd {
	if s < 0 || s >= settingsSectionCount {
		return nil
	}
	if m.section >= 0 && int(m.section) < len(m.cursors) {
		m.cursors[m.section] = m.cursor
	}
	m.section = s
	m.cursor = m.cursors[s]
	if !m.visited[s] {
		m.visited[s] = true
		m.cursor = m.homeCursor()
	}
	m.offset = 0
	m.message = ""
	m.clampCursor()
	m.ensureCursorVisible()
	if s == sectionMeeting && !m.routeDestsLoading && m.routeDests == nil {
		return m.loadRouteDestinations()
	}
	return nil
}
