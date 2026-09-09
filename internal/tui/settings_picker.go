package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

// settingsPicker is the list behind a pick or menu row, opened with enter.
// While it is up it owns every key: typing filters, ↑/↓ move, enter chooses,
// tab previews where the row can, esc cancels. Typing into a filter is the
// modal boundary this screen was missing — a letter can no longer walk you
// two sections away.
type settingsPicker struct {
	active  bool
	title   string
	options []string
	// current is the option that is the value today (✓), or -1.
	current int
	query   string
	// matches are the option indices the query keeps, in order.
	matches []int
	cursor  int
	offset  int
	set     func(*settingsModel, int) tea.Cmd
	preview func(*settingsModel, int) tea.Cmd
}

func (m *settingsModel) openPicker(row settingRow) tea.Cmd {
	if len(row.options) == 0 {
		if row.reason != "" {
			m.message = row.reason
		}
		return nil
	}
	p := settingsPicker{
		active:  true,
		title:   row.name,
		options: row.options,
		current: row.current,
		set:     row.set,
		preview: row.preview,
	}
	p.refilter()
	if row.current >= 0 && row.current < len(row.options) {
		p.cursor = row.current
	}
	m.picker = p
	m.message = ""
	m.ensurePickerVisible()
	return nil
}

func (m *settingsModel) closePicker() {
	m.picker = settingsPicker{}
}

// refilter keeps the options every whitespace-separated term of the query
// matches, case-insensitively, and keeps the cursor on a row.
func (p *settingsPicker) refilter() {
	terms := strings.Fields(strings.ToLower(p.query))
	p.matches = p.matches[:0]
	for i, option := range p.options {
		lower := strings.ToLower(option)
		keep := true
		for _, term := range terms {
			if !strings.Contains(lower, term) {
				keep = false
				break
			}
		}
		if keep {
			p.matches = append(p.matches, i)
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = max(len(p.matches)-1, 0)
	}
	p.offset = 0
}

func (m settingsModel) pickerRows() int {
	// title, filter, rule, counter
	return max(m.visibleRows()-4, 1)
}

func (m *settingsModel) ensurePickerVisible() {
	p := &m.picker
	rows := m.pickerRows()
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	p.offset = max(p.offset, 0)
}

func (m settingsModel) updatePicker(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	p := &m.picker
	switch msg.String() {
	case "esc":
		m.closePicker()
		return m, nil
	case "enter":
		if len(p.matches) == 0 {
			return m, nil
		}
		idx, set := p.matches[p.cursor], p.set
		m.closePicker()
		if set == nil {
			return m, nil
		}
		cmd := set(&m, idx)
		return m, cmd
	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p.cursor < len(p.matches)-1 {
			p.cursor++
		}
	case "pgup":
		p.cursor = max(p.cursor-m.pickerRows(), 0)
	case "pgdown":
		p.cursor = min(p.cursor+m.pickerRows(), max(len(p.matches)-1, 0))
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = max(len(p.matches)-1, 0)
	case "tab":
		if p.preview != nil && len(p.matches) > 0 {
			preview, idx := p.preview, p.matches[p.cursor]
			cmd := preview(&m, idx)
			return m, cmd
		}
	case "backspace", "ctrl+h":
		if p.query != "" {
			_, size := utf8.DecodeLastRuneInString(p.query)
			p.query = p.query[:len(p.query)-size]
			p.refilter()
		}
	case "ctrl+u":
		p.query = ""
		p.refilter()
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.query += msg.String()
			p.refilter()
		}
	}
	m.ensurePickerVisible()
	return m, nil
}

// pickerLines replaces the section's rows while the picker is up.
func (m settingsModel) pickerLines() []string {
	p := m.picker
	width := m.renderWidth()
	lines := []string{
		"  " + headerStyle.Render(p.title) + dimStyle.Render(fmt.Sprintf(" · %d options", len(p.options))),
		"  " + dimStyle.Render("filter ") + normalStyle.Render(p.query) + "▊",
		dimStyle.Render("  " + strings.Repeat("─", max(min(width-4, 72), 1))),
	}
	rows := m.pickerRows()
	end := min(p.offset+rows, len(p.matches))
	for i := p.offset; i < end; i++ {
		idx := p.matches[i]
		cursor, style := "  ", normalStyle
		if i == p.cursor {
			cursor, style = "▸ ", selectedStyle
		}
		label := p.options[idx]
		if idx == p.current {
			label += " ✓"
		}
		lines = append(lines, ansi.Truncate("  "+cursor+style.Render(label), width, "…"))
	}
	if len(p.matches) == 0 {
		lines = append(lines, dimStyle.Render("  nothing matches"))
	}
	counter := fmt.Sprintf("%d of %d", len(p.matches), len(p.options))
	if len(p.matches) > 0 {
		counter = fmt.Sprintf("%d of %d", p.cursor+1, len(p.matches))
		if len(p.matches) != len(p.options) {
			counter += fmt.Sprintf(" · %d match", len(p.matches))
		}
	}
	lines = append(lines, dimStyle.Render("  "+counter))
	return lines
}
