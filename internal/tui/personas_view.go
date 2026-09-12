package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func (m personasModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("  Personas"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  Switch agents · create/edit system prompts the brain actually loads", width, "…")))
	b.WriteString("\n")
	if m.height == 0 || m.height >= 10 {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	if m.formMode != "" {
		// Form is never pad-truncated: the prompt field was easy to clip when we
		// reused the list body height budget.
		for _, line := range m.formLines() {
			b.WriteString(ansi.Truncate(line, width, "…"))
			b.WriteString("\n")
		}
	} else {
		listRows := m.visibleRows()
		lines := m.listLines(listRows)
		if m.confirmDeleteID != "" {
			lines = m.confirmDeleteLines(listRows)
		}
		for _, line := range lines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if m.loadWarning != "" {
		b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.loadWarning), width, "…"))
		b.WriteString("\n")
	}
	if m.message != "" {
		b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
	}
	help := "  ↑/↓ • enter switch • n create • e edit • m model & voice • d delete • esc back"
	switch {
	case m.formMode != "" && m.promptOpen:
		help = "  esc NORMAL • :w back to form • :wq save • ctrl+j save • :q discard"
	case m.formMode != "" && m.formRow == formRowVoice:
		help = "  ↑/↓ move • ←/→ change • p preview • enter edit prompt • ctrl+j save • esc cancel"
	case m.formMode != "":
		help = "  ↑/↓ move • ←/→ change • enter edit prompt • ctrl+j save • esc cancel"
	case m.confirmDeleteID != "":
		help = "  y delete permanently • any other key cancels"
	}
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	return b.String()
}

func (m personasModel) listLines(listRows int) []string {
	if m.loadErr != "" {
		return padPersonasLines([]string{"  error loading personas: " + m.loadErr}, listRows)
	}
	active := ""
	if m.cfg != nil {
		active = persona.ActiveID(m.cfg)
	}
	total := m.listLen()
	start := m.offset
	end := min(start+listRows, total)
	lines := make([]string, 0, listRows)
	for i := start; i < end; i++ {
		if i == len(m.items) {
			lines = append(lines, m.row(i, personasCreateLabel))
			continue
		}
		p := m.items[i]
		mark := ""
		if p != nil && p.ID == active {
			mark = " ✓"
		}
		lines = append(lines, m.row(i, personaListLabel(p)+mark))
	}
	return padPersonasLines(lines, listRows)
}

// formBoxWidth is the outer width of the form's field boxes, leaving a
// 2-column indent inside the terminal.
func (m personasModel) formBoxWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return max(min(w-2, 78), 24)
}

// formBox draws a titled, rounded box around body lines. The title is already
// styled; the border tracks focus (accent when active, dim otherwise).
func formBox(title string, body []string, width int, focused bool) []string {
	border := dimStyle
	if focused {
		border = lipgloss.NewStyle().Foreground(colorAccent)
	}
	return formBoxStyled(title, body, width, border)
}

// formBoxStyled is formBox with the border style chosen by the caller, so a
// destructive prompt can wear a colour the ordinary form never uses.
func formBoxStyled(title string, body []string, width int, border lipgloss.Style) []string {
	inner := width - 4
	titleText := " " + title + " "
	fill := max(width-3-ansi.StringWidth(titleText), 0)
	lines := make([]string, 0, len(body)+2)
	lines = append(lines, "  "+border.Render("╭─")+titleText+border.Render(strings.Repeat("─", fill)+"╮"))
	for _, line := range body {
		content := ansi.Truncate(line, inner, "…")
		pad := max(inner-ansi.StringWidth(content), 0)
		lines = append(lines, "  "+border.Render("│")+" "+content+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	lines = append(lines, "  "+border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return lines
}

func (m personasModel) formLines() []string {
	if m.promptOpen {
		return m.promptEditorLines()
	}
	boxW := m.formBoxWidth()

	nameTitle := headerStyle.Render("Name")
	if m.formMode == "create" {
		slug := persona.Slugify(m.nameInput.Value())
		if slug == "" {
			slug = "persona"
		}
		nameTitle += dimStyle.Render(" · id: " + slug)
	}
	stackTitle := headerStyle.Render("Model & voice") + dimStyle.Render(" · (default) inherits Settings")
	promptTitle := headerStyle.Render("System prompt") + dimStyle.Render(" · {agent_name} supported")

	lines := []string{
		"  " + normalStyle.Bold(true).Render(m.formTitle()),
		"",
	}
	lines = append(lines, formBox(nameTitle, []string{m.nameInput.View()}, boxW, m.formRow == formRowName)...)
	lines = append(lines, formBox(stackTitle, m.stackLines(), boxW, isStackRow(m.formRow))...)
	lines = append(lines, formBox(promptTitle, m.promptSummaryLines(), boxW, m.formRow == formRowPrompt)...)
	return lines
}

// formTitle names what the form is editing.
func (m personasModel) formTitle() string {
	if m.formMode == "edit" {
		return "Edit persona " + m.editID
	}
	return "Create a new voice agent"
}

// promptEditorLines is the system prompt with the screen to itself. The other
// fields are not drawn: the editor is the only thing taking keys here, and it
// was easy to clip when it shared the body height budget.
func (m personasModel) promptEditorLines() []string {
	title := headerStyle.Render("System prompt") + dimStyle.Render(" · {agent_name} supported") +
		dimStyle.Render(" — ") + m.promptTA.modeChip()
	lines := []string{
		"  " + normalStyle.Bold(true).Render(m.formTitle()),
		"",
	}
	lines = append(lines, formBox(title, strings.Split(m.promptTA.View(), "\n"), m.formBoxWidth(), true)...)
	return append(lines, dimStyle.Render("  "+m.promptTA.modeline()))
}

// promptSummaryLines is the collapsed prompt row: what the document opens with
// and how big it is, so the field says what it holds without taking the screen.
func (m personasModel) promptSummaryLines() []string {
	body := strings.TrimRight(m.promptTA.Value(), "\n")
	if strings.TrimSpace(body) == "" {
		return []string{dimStyle.Render("(empty) · enter to write one")}
	}
	rows := strings.Split(body, "\n")
	first := ""
	for _, line := range rows {
		if strings.TrimSpace(line) != "" {
			first = strings.TrimSpace(line)
			break
		}
	}
	count := fmt.Sprintf("%d lines", len(rows))
	if len(rows) == 1 {
		count = "1 line"
	}
	return []string{
		normalStyle.Render(first),
		dimStyle.Render(count + " · enter to edit"),
	}
}

// stackLines renders the model & voice rows of the form.
func (m personasModel) stackLines() []string {
	row := func(idx int, label, value string) string {
		mark, labelStyle := "  ", dimStyle
		if m.formRow == idx {
			mark, labelStyle = selectedStyle.Render("▸ "), selectedStyle
		}
		return mark + labelStyle.Render(fmt.Sprintf("%-*s", stackLabelWidth, label)) + " " + value
	}
	brainProvider := stackBrainProviders()[m.brainProviderIdx]
	brainRow := row(formRowBrainProvider, "Brain", "‹ "+brainProvider+" ›")
	if strings.EqualFold(brainProvider, "claude") && strings.TrimSpace(m.brainModelInput.Value()) != "" {
		// Claude has no app-level model key yet; be honest that the model
		// string is recorded on the profile but not routed.
		brainRow += dimStyle.Render("  (model saved, not routed for claude yet)")
	}
	ttsProvider := stackTTSProviders()[m.ttsProviderIdx]
	voices := m.stackVoiceList()
	voiceLabel := stackDefaultLabel
	if idx := m.stackVoiceIndex(); idx >= 0 && idx < len(voices) {
		voiceLabel = voices[idx]
	}
	tierValue := dimStyle.Render("— (qwen3-tts only)")
	if m.stackTierUsable() {
		tiers := m.stackTierList()
		tierLabel := stackDefaultLabel
		if idx := m.stackTierIndex(); idx >= 0 && idx < len(tiers) {
			tierLabel = tiers[idx]
		}
		tierValue = "‹ " + tierLabel + " ›"
	}
	workerProvider := stackWorkerProviders()[m.workerProviderIdx]
	workerRow := row(formRowWorkerProvider, "Worker", "‹ "+workerProvider+" ›")
	if workerProvider == stackDefaultLabel && m.cfg != nil && !config.WorkerSelected(m.cfg) {
		// "(default)" is only honest when Settings actually holds a choice.
		workerRow += dimStyle.Render("  (Settings has no worker yet — inherits " + config.VoiceWorkerProviderOf(m.cfg) + ")")
	}
	workerModelRow := row(formRowWorkerModel, "Worker model", "‹ "+m.workerModelLabel()+" ›")
	if strings.EqualFold(m.effectiveStackWorkerProvider(), "claude") && strings.TrimSpace(m.workerModel) != "" {
		workerModelRow += dimStyle.Render("  (model saved, not routed for claude yet)")
	}
	return []string{
		brainRow,
		row(formRowBrainModel, "Brain model", m.stackModelRowValue()),
		workerRow,
		workerModelRow,
		row(formRowTTSProvider, "TTS", "‹ "+ttsProvider+" ›"),
		row(formRowVoice, "Voice", "‹ "+voiceLabel+" ›"),
		row(formRowTier, "Tier", tierValue),
	}
}

func (m personasModel) row(i int, label string) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	prefix := "  "
	style := dimStyle
	if i == m.cursor {
		prefix = "▸ "
		style = selectedStyle
	}
	return style.Render(ansi.Truncate(prefix+label, width, "…"))
}

func padPersonasLines(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}
