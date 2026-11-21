package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// openView reads the session fresh from the store: the list snapshot may be
// older than the file. Nothing is started; the transcript is only displayed.
func (m *sessionsModel) openView(id string) {
	load := m.loadSession
	if load == nil {
		load = session.Load
	}
	s, err := load(id)
	if err != nil {
		m.message, m.messageErr = "Could not view: "+err.Error(), true
		return
	}
	m.viewing = s
	m.viewport = viewport.New(1, 1)
	// d deletes here, so half pages stay on the ctrl keys alone.
	m.viewport.KeyMap.HalfPageUp.SetKeys("ctrl+u")
	m.viewport.KeyMap.HalfPageDown.SetKeys("ctrl+d")
	m.layoutView()
	m.viewport.GotoTop()
}

func (m sessionsModel) updateView(msg tea.KeyMsg) (sessionsModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		id := m.viewing.ID
		return m, func() tea.Msg { return startPipelineMsg{sessionID: id} }
	case "d":
		m.confirmID = m.viewing.ID
		return m, nil
	case "esc", "q":
		m.viewing = nil
		return m, nil
	case "home", "g":
		m.viewport.GotoTop()
		return m, nil
	case "end", "G":
		m.viewport.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *sessionsModel) layoutView() {
	m.viewport.Width = m.viewWidth()
	// The list's row budget, so the list and the transcript share chrome.
	m.viewport.Height = m.visibleRows()
	m.viewport.SetContent(m.transcriptContent())
	// SetContent leaves an offset past the new last page, which shows blank
	// rows after the window grows.
	m.viewport.SetYOffset(m.viewport.YOffset)
}

// transcriptContent renders saved turns with the conversation's own bubbles so
// a viewed session reads like the resumed one.
func (m sessionsModel) transcriptContent() string {
	width := max(m.viewWidth()-2, 1)
	// Wrap inside the bubble frame, as the worker thread does, so a bubble is
	// never wider than the screen.
	inner := max(1, width-agentBubbleStyle.GetHorizontalFrameSize())
	fallback := strings.TrimSpace(m.viewing.SelectedPersonaDisplayName)
	if fallback == "" {
		fallback = strings.TrimSpace(m.agentName)
	}
	if fallback == "" {
		fallback = "Assistant"
	}
	// Resumed conversations seed speaker-1 with the user's name the same way.
	names := speaker.NewNameMap()
	names.SeedPrimary(m.userName)
	var parts []string
	for _, t := range m.viewing.Turns {
		var block string
		switch t.Role {
		case "user":
			block = renderSpeakerUserTurn(names.Display(t.Speaker), ansi.Wrap(storedUserText(t), inner, ""))
		case "assistant":
			item := storedAgentItem(t, fallback)
			item.kind = chatItemAgent
			item.think = ansi.Wrap(item.think, inner, "")
			item.spoken = ansi.Wrap(item.spoken, inner, "")
			block = item.render(false, width)
			if block == "" {
				continue
			}
		default:
			continue
		}
		for _, ref := range t.Attachments {
			block += "\n" + dimStyle.Render(ansi.Truncate(mediaSummary(ref), width, "…"))
		}
		parts = append(parts, block)
	}
	if len(parts) == 0 {
		return dimStyle.Render("  No turns to show.")
	}
	lines := strings.Split(strings.Join(parts, "\n"), "\n")
	// Bubbles carry a top margin; the header already separates the first one.
	for len(lines) > 1 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for i, line := range lines {
		lines[i] = "  " + ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (m sessionsModel) transcriptView() string {
	var b strings.Builder
	width := m.viewWidth()
	s := m.viewing
	// Cut the text, not the styled block: the title's margin row would
	// otherwise take the ellipsis.
	title := ansi.Truncate("  "+sessionSummary(*s), width, "…")
	if m.compact() {
		b.WriteString(headerStyle.Render(title))
		b.WriteString("\n")
	} else {
		b.WriteString(titleStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render(ansi.Truncate("  "+transcriptMeta(*s), width, "…")))
		b.WriteString("\n\n")
	}
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(m.footer("  ↑/↓ scroll • enter open • d delete • esc back"))
	return b.String()
}

func transcriptMeta(s session.Session) string {
	parts := []string{turnCount(len(s.Turns)), compactAge(s.UpdatedAt)}
	if name := strings.TrimSpace(s.SelectedPersonaDisplayName); name != "" {
		parts = append(parts, name)
	}
	provider, model := strings.TrimSpace(s.Provider), strings.TrimSpace(s.Model)
	switch {
	case provider != "" && model != "":
		parts = append(parts, provider+" / "+model)
	case provider != "":
		parts = append(parts, provider)
	case model != "":
		parts = append(parts, model)
	}
	return strings.Join(parts, " · ")
}
