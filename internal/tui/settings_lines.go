package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A line is what the cursor rests on. A list row (pick, menu) contributes a
// heading, one line per option and a blank after it; every other row is one
// line, except a choice row whose options wrap onto more. Headings, blanks
// and wrapped options are skipped by the cursor, so ↑/↓ only ever land on
// something enter can act on.
type settingLine struct {
	row int
	// option indexes the row's options on a list, or is -1 for the row itself.
	option int
	header bool
	blank  bool
	// wrap > 0 is a choice row's wrap-th continuation: more of its options.
	wrap int
}

func (l settingLine) focusable() bool { return !l.header && !l.blank && l.wrap == 0 }

// expands reports whether a row draws its options under a heading. A list
// with nothing in it, or one that is disabled, draws as a single line that
// says why.
func (r settingRow) expands() bool {
	return (r.kind == rowPick || r.kind == rowMenu) && !r.disabled && len(r.options) > 0
}

// linesFor lays rows out with valueWidth cells for a row's value; a choice
// row that needs more takes continuation lines.
func linesFor(rows []settingRow, valueWidth int) []settingLine {
	var lines []settingLine
	blank := func(i int) {
		if n := len(lines); n > 0 && !lines[n-1].blank {
			lines = append(lines, settingLine{row: i, option: -1, blank: true})
		}
	}
	for i, row := range rows {
		if !row.expands() {
			lines = append(lines, settingLine{row: i, option: -1})
			for w := 1; w < len(row.valueLines(valueWidth)); w++ {
				lines = append(lines, settingLine{row: i, option: -1, wrap: w})
			}
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

// nameColumn is the width of the name column one-line rows share.
func nameColumn(rows []settingRow) int {
	width := 0
	for _, row := range rows {
		if !row.expands() {
			width = max(width, lipgloss.Width(row.name))
		}
	}
	return min(max(width, 12), 30) + 2
}

// valueWidth is the room a one-line row's value has: the content less the
// margin, the cursor and the name column.
func (m settingsModel) valueWidth(rows []settingRow) int {
	return m.contentWidth() - 4 - nameColumn(rows)
}

// layout is the current section's rows and the lines they take at this width.
func (m settingsModel) layout() ([]settingRow, []settingLine) {
	rows := m.sectionRows()
	return rows, linesFor(rows, m.valueWidth(rows))
}

func (m settingsModel) sectionLines() []settingLine {
	_, lines := m.layout()
	return lines
}

// focused is the row under the cursor and, on a list, which of its options.
func (m settingsModel) focused() (settingRow, settingLine, bool) {
	rows, lines := m.layout()
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
	rows, lines := m.layout()
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

// lineAnchor is the line the cursor rests on, by what is drawn there rather
// than by index: a catalog landing or a resize moves lines, not choices.
type lineAnchor struct {
	row    int
	name   string
	option int
	// voice names the voice under the cursor: a landing catalog reorders the
	// voice list, so its index is no anchor.
	voice string
}

func (m settingsModel) anchor() (lineAnchor, bool) {
	rows, lines := m.layout()
	if m.cursor < 0 || m.cursor >= len(lines) || !lines[m.cursor].focusable() {
		return lineAnchor{}, false
	}
	l := lines[m.cursor]
	a := lineAnchor{row: l.row, name: rows[l.row].name, option: l.option}
	if m.section == sectionVoice {
		a.voice, _ = m.focusedVoice()
	}
	return a, true
}

// restore puts the cursor back on a's line: the same row, or the first of
// that name when rows were added above it; a voice that left the list gives
// way to the ✓ voice.
func (m *settingsModel) restore(a lineAnchor) {
	option := a.option
	if a.voice != "" {
		shown := m.shownVoiceItems()
		if option = voiceIndex(shown, a.voice); option < 0 {
			option = voiceIndex(shown, m.activeVoice())
		}
	}
	rows, lines := m.layout()
	found := -1
	for i, l := range lines {
		if !l.focusable() || rows[l.row].name != a.name || l.option != option {
			continue
		}
		if l.row == a.row {
			found = i
			break
		}
		if found < 0 {
			found = i
		}
	}
	if found >= 0 {
		m.cursor = found
	}
	m.clampCursor()
}

// keepFocus runs change, then puts every visited section's cursor back on
// the line it rested on, the remembered ones included.
func (m *settingsModel) keepFocus(change func()) {
	anchors := map[settingsSection]lineAnchor{}
	m.eachVisited(func() {
		if a, ok := m.anchor(); ok {
			anchors[m.section] = a
		}
	})
	change()
	m.eachVisited(func() {
		if a, ok := anchors[m.section]; ok {
			m.restore(a)
		} else {
			m.clampCursor()
		}
	})
	m.ensureCursorVisible()
}

// eachVisited runs f with each visited section current and its cursor in
// m.cursor, then returns to the section that was current.
func (m *settingsModel) eachVisited(f func()) {
	section := m.section
	m.cursors[section] = m.cursor
	for s := range settingsSectionCount {
		if !m.visited[s] {
			continue
		}
		m.section, m.cursor = s, m.cursors[s]
		f()
		m.cursors[s] = m.cursor
	}
	m.section, m.cursor = section, m.cursors[section]
}

// resize changes the geometry; a choice row may wrap differently at the new
// width, so the cursors are kept on their lines.
func (m *settingsModel) resize(width, height int) {
	m.keepFocus(func() { m.width, m.height = width, height })
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
