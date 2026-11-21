package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type sessionsModel struct {
	sessions []session.Session
	cursor   int
	width    int
	height   int
	offset   int

	// agentName labels replies saved without a persona name; userName is the
	// configured name for speaker-1.
	agentName string
	userName  string
	// loadSession and deleteSession default to the user's session store.
	loadSession   func(id string) (*session.Session, error)
	deleteSession func(id string) error
	// track keeps a delete running through shutdown; nil runs it untracked.
	track func(tea.Cmd) tea.Cmd

	confirmID  string
	deletingID string
	message    string
	messageErr bool

	viewing  *session.Session
	viewport viewport.Model
}

// sessionDeletedMsg reports a delete started from the sessions screen.
type sessionDeletedMsg struct {
	id  string
	err error
}

func newSessions(sessions []session.Session) sessionsModel {
	return sessionsModel{sessions: resumableSessions(sessions)}
}

func resumableSessions(sessions []session.Session) []session.Session {
	out := make([]session.Session, 0, len(sessions))
	for _, saved := range sessions {
		if len(saved.Turns) > 0 {
			out = append(out, saved)
		}
	}
	return out
}

// enter starts the screen on the list with no leftover prompt or transcript.
func (m sessionsModel) enter(width, height int) sessionsModel {
	m.width, m.height = width, height
	m.viewing = nil
	m.confirmID = ""
	m.message, m.messageErr = "", false
	m.ensureVisible()
	return m
}

func (m sessionsModel) Update(msg tea.Msg) (sessionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		if m.viewing != nil {
			m.layoutView()
		}
	case sessionDeletedMsg:
		m.applyDeleted(msg)
	case tea.KeyMsg:
		if m.deletingID != "" {
			return m, nil
		}
		m.message, m.messageErr = "", false
		if m.confirmID != "" {
			id := m.confirmID
			m.confirmID = ""
			if s := msg.String(); s == "y" || s == "Y" {
				cmd := m.startDelete(id)
				return m, cmd
			}
			return m, nil
		}
		if m.viewing != nil {
			return m.updateView(msg)
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(len(m.sessions)-1, 0)
		case "enter":
			if len(m.sessions) > 0 {
				id := m.sessions[m.cursor].ID
				return m, func() tea.Msg { return startPipelineMsg{sessionID: id} }
			}
		case "v":
			if len(m.sessions) > 0 {
				m.openView(m.sessions[m.cursor].ID)
			}
		case "d":
			if len(m.sessions) > 0 {
				m.confirmID = m.sessions[m.cursor].ID
			}
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		m.ensureVisible()
	}
	return m, nil
}

func (m *sessionsModel) startDelete(id string) tea.Cmd {
	del := m.deleteSession
	if del == nil {
		del = func(id string) error { return session.DefaultStore().Delete(id) }
	}
	m.deletingID = id
	cmd := func() tea.Msg { return sessionDeletedMsg{id: id, err: del(id)} }
	if m.track != nil {
		return m.track(cmd)
	}
	return cmd
}

// applyDeleted drops the deleted row. A failed delete keeps it so the user can
// retry; a session already gone from disk is dropped like a success.
func (m *sessionsModel) applyDeleted(msg sessionDeletedMsg) {
	if msg.id == m.deletingID {
		m.deletingID = ""
	}
	idx := m.indexOf(msg.id)
	if idx < 0 {
		return
	}
	summary := sessionSummary(m.sessions[idx])
	if msg.err != nil && !errors.Is(msg.err, session.ErrSessionNotFound) {
		m.message, m.messageErr = "Could not delete: "+msg.err.Error(), true
		return
	}
	m.sessions = append(m.sessions[:idx:idx], m.sessions[idx+1:]...)
	if idx < m.cursor {
		m.cursor--
	}
	m.cursor = min(m.cursor, max(len(m.sessions)-1, 0))
	if m.viewing != nil && m.viewing.ID == msg.id {
		m.viewing = nil
	}
	m.message, m.messageErr = fmt.Sprintf("Deleted \"%s\"", summary), false
	m.ensureVisible()
}

func (m sessionsModel) indexOf(id string) int {
	for i, s := range m.sessions {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (m *sessionsModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(len(m.sessions)-visible, 0)
	m.offset = min(max(m.offset, 0), maxOffset)
}

func (m sessionsModel) visibleRows() int {
	// Title + subtitle + blank + footer consume ~5 rows on a normal terminal;
	// compact mode keeps only the header/footer chrome.
	h := m.height
	if h <= 0 {
		// No WindowSize yet — still show a full-ish list rather than three rows.
		h = 24
	}
	if h < 10 {
		return max(h-2, 1)
	}
	return max(h-6, 1)
}

func (m sessionsModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m sessionsModel) compact() bool {
	return m.height > 0 && m.height < 10
}

func (m sessionsModel) View() string {
	if m.viewing != nil {
		return m.transcriptView()
	}
	var b strings.Builder
	width := m.viewWidth()
	compact := m.compact()
	title := titleStyle.Render("  Sessions")
	if compact {
		title = headerStyle.Render("  Sessions")
	}
	b.WriteString(ansi.Truncate(title, width, "…"))
	b.WriteString("\n")
	if !compact {
		b.WriteString(ansi.Truncate(subtitleStyle.Render("  Open, view, or delete a saved conversation"), width, "…"))
		b.WriteString("\n\n")
	}
	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  No saved sessions."))
		b.WriteString("\n")
	} else {
		visible := m.visibleRows()
		end := min(m.offset+visible, len(m.sessions))
		for i := m.offset; i < end; i++ {
			s := m.sessions[i]
			cursor, style := "  ", normalStyle
			if i == m.cursor {
				cursor, style = "▸ ", selectedStyle
			}
			age := compactAge(s.UpdatedAt)
			line := fmt.Sprintf("%s  %d turns  %s", sessionSummary(s), len(s.Turns), age)
			maxWidth := max(width-6, 1)
			line = ansi.Truncate(line, maxWidth, "…")
			b.WriteString("  " + cursor + style.Render(line) + "\n")
		}
	}
	b.WriteString(m.footer("  ↑/↓ navigate • enter open • v view • d delete • esc back"))
	return b.String()
}

// footer renders the bottom chrome. A message takes the blank separator line,
// or the hint line in compact mode, so the rows above never move.
func (m sessionsModel) footer(hint string) string {
	width := m.viewWidth()
	line := func(style func(...string) string, text string) string {
		return style(ansi.Truncate(text, width, "…"))
	}
	var msg string
	if m.message != "" {
		style := dimStyle.Render
		if m.messageErr {
			style = errorStyle.Render
		}
		msg = line(style, "  "+strings.Join(strings.Fields(m.message), " "))
	}
	bottom := line(dimStyle.Render, hint)
	switch {
	case m.deletingID != "":
		bottom = line(dimStyle.Render, "  Deleting…")
	case m.confirmID != "":
		bottom = line(warningStyle.Render, fmt.Sprintf("  Delete \"%s\"? y delete • any other key cancel", m.confirmSummary()))
	case m.compact() && msg != "":
		return msg
	}
	if m.compact() {
		return bottom
	}
	return msg + "\n" + bottom
}

func (m sessionsModel) confirmSummary() string {
	summary := "Untitled conversation"
	if idx := m.indexOf(m.confirmID); idx >= 0 {
		summary = sessionSummary(m.sessions[idx])
	}
	return ansi.Truncate(summary, 40, "…")
}

func sessionSummary(s session.Session) string {
	summary := strings.Join(strings.Fields(s.Summary), " ")
	if summary == "" {
		return "Untitled conversation"
	}
	return summary
}

func compactAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
