package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

func (m settingsModel) View() string {
	width := m.renderWidth()
	compact := m.isCompact()
	listRows := m.visibleRows()

	var parts []string
	parts = append(parts, headerStyle.Render("  Settings"))

	tabs := []string{"Brain", "Brain model", "Worker", "Tools", "TTS", "Qwen", "Voice", "Language", "STT", "Speakers", "Input", "Output", "Meeting"}
	var tabLine strings.Builder
	for i, tab := range tabs {
		style := dimStyle
		if settingsSection(i) == m.section {
			style = selectedStyle
		}
		if i > 0 {
			tabLine.WriteString("  ")
		}
		tabLine.WriteString(style.Render(tab))
	}
	parts = append(parts, ansi.Truncate("  "+tabLine.String(), width, "…"))
	if !compact {
		// Full terminal width so the chrome tracks resizes/splits.
		parts = append(parts, dimStyle.Render(strings.Repeat("─", max(width, 1))))
	}

	var listLines []string
	switch {
	case m.enroll.active:
		listLines = m.enrollOverlayLines()
	case m.confirmRemoveVoice != "":
		listLines = m.confirmRemoveVoiceLines()
	case m.picker.active:
		listLines = m.pickerLines()
	default:
		listLines = m.rowLines()
	}
	if m.edit.kind != editNone {
		listLines = append([]string{m.editorLine(), ""}, listLines...)
	}
	// Fill the list region so short sections still expand with the terminal.
	if len(listLines) > listRows {
		listLines = listLines[:listRows]
	}
	for len(listLines) < listRows {
		listLines = append(listLines, "")
	}
	parts = append(parts, listLines...)

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

// rowLines draws the section: one line per setting, name then value, the
// verb glyphs on the value saying what ←/→ and enter will do.
func (m settingsModel) rowLines() []string {
	rows := m.sectionRows()
	if len(rows) == 0 {
		return nil
	}
	nameWidth := 0
	for _, row := range rows {
		nameWidth = max(nameWidth, lipgloss.Width(row.name))
	}
	nameWidth = min(max(nameWidth, 12), 30) + 2
	start, end := m.visibleRange(len(rows))
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, m.rowLine(i, rows[i], nameWidth))
	}
	return lines
}

func (m settingsModel) rowLine(idx int, row settingRow, nameWidth int) string {
	cursor := "  "
	nameStyle := normalStyle
	if row.kind == rowInfo || row.disabled {
		nameStyle = dimStyle
	}
	if idx == m.cursor {
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
	return ansi.Truncate(line, m.renderWidth(), "…")
}

// rowValue is the value column, drawn so the verb is legible before any key
// is pressed: ‹ › means ←/→ step it, ON/OFF means it flips, a bar means it
// slides, an ellipsis means enter opens something.
func (m settingsModel) rowValue(row settingRow) string {
	if row.disabled {
		return dimStyle.Render("— " + row.reason)
	}
	switch row.kind {
	case rowPick:
		return dimStyle.Render("‹ ") + normalStyle.Render(row.value) + dimStyle.Render(" ›")
	case rowToggle:
		if row.value == "ON" {
			return statusStyle.Render("ON")
		}
		return dimStyle.Render("OFF")
	case rowSlider, rowText:
		return normalStyle.Render(row.value)
	case rowMenu:
		return dimStyle.Render("…")
	case rowAction:
		return dimStyle.Render(row.value)
	case rowInfo:
		return dimStyle.Render(row.value)
	}
	return normalStyle.Render(row.value)
}

// detailLine is what the focused row does and when a change applies — facts
// that stay on screen, unlike a status message.
func (m settingsModel) detailLine() string {
	row, ok := m.focusedRow()
	if !ok || m.picker.active {
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
	case m.picker.active:
		help := "  type to filter • ↑/↓ move • enter choose"
		if m.picker.preview != nil {
			help += " • tab preview"
		}
		return help + " • esc cancel"
	}
	parts := []string{"↑/↓ move"}
	if row, ok := m.focusedRow(); ok && !row.disabled {
		switch row.kind {
		case rowPick:
			parts = append(parts, "←/→ change", fmt.Sprintf("enter list (%d)", len(row.options)))
			if row.preview != nil {
				parts = append(parts, "p preview")
			}
		case rowToggle:
			parts = append(parts, "←/→ or space toggle")
		case rowSlider:
			parts = append(parts, "←/→ adjust")
		case rowText:
			parts = append(parts, "enter edit")
		case rowAction, rowMenu, rowInfo:
			verb := row.verb
			if verb == "" {
				verb = "select"
			}
			parts = append(parts, "enter "+verb)
		}
	}
	parts = append(parts, "tab section", "esc back")
	return "  " + strings.Join(parts, " • ")
}

func (m settingsModel) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}
