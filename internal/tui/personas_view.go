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
	b.WriteString(dimStyle.Render(ansi.Truncate("  Choose who responds · compare models and voices", width, "…")))
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
		listRows := m.listBodyRows()
		lines := m.listLines(listRows)
		if m.confirmDeleteID != "" {
			lines = m.confirmDeleteLines(listRows)
		}
		for _, line := range lines {
			b.WriteString(ansi.Truncate(line, width, "…"))
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
	help := "  ↑/↓ • enter switch • x enable/disable • e edit • n create • m model • d delete • esc back"
	switch {
	case m.formMode != "" && m.promptOpen:
		help = "  esc NORMAL • :w back to form • :wq save • ctrl+j save • :q discard"
	case m.formMode != "" && m.formRow == formRowVoice:
		help = "  ↑/↓ move • ←/→ change • p preview/stop • enter edit prompt • ctrl+j save • esc cancel"
	case m.formMode != "":
		help = "  ↑/↓ move • ←/→ change • enter edit prompt • ctrl+j save • esc cancel"
	case m.confirmDeleteID != "":
		help = "  y delete permanently • any other key cancels"
	}
	if m.formMode == "" && m.confirmDeleteID == "" && m.screenHeight() >= 16 {
		b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ navigate · enter switch · m model · e edit", width, "…")))
		b.WriteString("\n")
		help = "  n create · x enable/disable · d delete · esc back"
	}
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	return b.String()
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
	directoryTitle := headerStyle.Render("Starting directory") + dimStyle.Render(" · (default) inherits app work_dir")
	promptTitle := headerStyle.Render("System prompt") + dimStyle.Render(" · {agent_name} supported")

	lines := []string{
		"  " + normalStyle.Bold(true).Render(m.formTitle()),
		"",
	}
	lines = append(lines, formBox(nameTitle, []string{m.nameInput.View()}, boxW, m.formRow == formRowName)...)
	lines = append(lines, formBox(stackTitle, m.stackLines(), boxW, isStackRow(m.formRow))...)
	lines = append(lines, formBox(directoryTitle, m.directoryLines(), boxW, m.formRow == formRowDirectory)...)
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
	voiceIdx := m.stackVoiceIndex()
	if voiceIdx >= 0 && voiceIdx < len(voices) {
		voiceLabel = voices[voiceIdx]
	}
	voiceValue := "‹ " + voiceLabel + " ›"
	if m.preview.previewing != "" {
		voiceValue += dimStyle.Render("  [p stop] ♫ " + m.preview.previewing)
	} else {
		voiceValue += dimStyle.Render("  [p preview]")
	}
	if n := len(voices) - 1; n > 8 && voiceIdx > 0 {
		// Long catalogs (Kokoro's 54, Phonon's 37 per language) need a
		// position so a user cycling with ←/→ knows where they are.
		voiceValue += dimStyle.Render(fmt.Sprintf("  %d/%d", voiceIdx, n))
	}
	languageValue := dimStyle.Render("— (one language)")
	if m.voiceLanguageUsable() {
		locales := m.voiceLocales()
		languageLabel := voiceLocaleAll
		if idx := m.voiceLocaleIndex(); idx > 0 && idx < len(locales) {
			languageLabel = locales[idx]
		}
		languageValue = "‹ " + languageLabel + " ›"
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
	lines := []string{
		brainRow,
		row(formRowBrainModel, "Brain model", m.stackModelRowValue()),
		workerRow,
		workerModelRow,
		row(formRowTTSProvider, "TTS", "‹ "+ttsProvider+" ›"),
		row(formRowVoice, "Voice", voiceValue),
		row(formRowVoiceLanguage, "Language", languageValue),
		row(formRowTier, "Tier", tierValue),
	}
	if detail := strings.TrimSpace(m.voiceDetail()); detail != "" {
		// Under the Voice row: what the catalog says about the pick, or why
		// the catalog is short. Width-bounded so a description never wraps.
		lines = insertAfter(lines, 5, "  "+dimStyle.Render(ansi.Truncate(detail, max(m.formInnerWidth()-4, 10), "…")))
	}
	return lines
}

// insertAfter inserts line after index i.
func insertAfter(lines []string, i int, line string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:i+1]...)
	out = append(out, line)
	return append(out, lines[i+1:]...)
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
