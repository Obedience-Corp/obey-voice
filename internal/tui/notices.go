package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const noticeLogCap = 20

// launcherNotice is one session event. Progress lines stay off this log.
type launcherNotice struct {
	id        int
	at        time.Time
	line      string
	err       bool
	progress  bool
	detail    []noticeDetail
	copyText  string
	copyLabel string
}

type noticeDetail struct {
	label string
	text  string
	dim   bool
	bad   bool
}

type noticeCopiedMsg struct {
	err error
}

func (m launcherModel) pushNotice(n launcherNotice) launcherModel {
	selected := 0
	if m.noticesOpen && m.noticeCursor >= 0 && m.noticeCursor < len(m.notices) {
		selected = m.notices[m.noticeCursor].id
	}
	m.noticeSeq++
	n.id = m.noticeSeq
	m.notices = append([]launcherNotice{n}, m.notices...)
	if len(m.notices) > noticeLogCap {
		m.notices = m.notices[:noticeLogCap]
	}
	if selected != 0 {
		idx := slices.IndexFunc(m.notices, func(item launcherNotice) bool {
			return item.id == selected
		})
		if idx >= 0 {
			m.noticeCursor = idx
			return m
		}
	}
	if m.noticesOpen {
		m.noticeCursor = 0
	}
	return m
}

func (m launcherModel) noticeRail(width int) string {
	if width <= 0 {
		return ""
	}
	var line string
	style := statusStyle
	switch {
	case m.progressLine != "":
		line = m.progressLine
		style = warningStyle
	case len(m.notices) > 0:
		n := m.notices[0]
		line = n.line
		if n.err {
			style = errorStyle
		}
	default:
		return ""
	}
	showKey := len(m.notices) > 0
	key := ""
	count := ""
	if showKey {
		key = lipgloss.NewStyle().Foreground(colorAccent).Background(colorRaised).Bold(true).Render(" n ")
	}
	if len(m.notices) > 1 {
		count = dimStyle.Render("  " + strconv.Itoa(len(m.notices)))
	}
	room := width - 2 - lipgloss.Width(key) - lipgloss.Width(count)
	if showKey && room < 8 {
		room = max(1, width-2)
		key = ""
		count = ""
	}
	body := ansi.Truncate(line, room, "…")
	return "  " + style.Render(body) + key + count
}

func (m launcherModel) noticeBrowser(width int) string {
	if width <= 2 || len(m.notices) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render(ansi.Truncate("  Notices", width, "…")))
	b.WriteString("\n\n")
	barW := width - 2
	if barW > 74 {
		barW = 74
	}
	sel := lipgloss.NewStyle().Bold(true).Foreground(colorBg).Background(colorAccent).Width(barW)
	idle := normalStyle.Width(barW)
	for i, n := range m.notices {
		label := n.line
		when := ""
		if !n.at.IsZero() {
			when = n.at.Format("15:04")
		}
		whenW := lipgloss.Width(when)
		labelRoom := barW - whenW - 3
		if labelRoom < 8 {
			labelRoom = max(1, barW-2)
			when = ""
			whenW = 0
		}
		label = ansi.Truncate(label, labelRoom, "…")
		gap := barW - lipgloss.Width(label) - whenW - 2
		if gap < 1 {
			gap = 1
		}
		row := " " + label + strings.Repeat(" ", gap) + when
		if i == m.noticeCursor {
			b.WriteString(sel.Render(row))
		} else if n.err {
			b.WriteString(lipgloss.NewStyle().Foreground(colorError).Width(barW).Render(row))
		} else {
			b.WriteString(idle.Render(row))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	cur := m.notices[min(m.noticeCursor, len(m.notices)-1)]
	rule := colorStatus
	if cur.err {
		rule = colorError
	}
	ruleStyle := lipgloss.NewStyle().Foreground(rule)
	for _, d := range cur.detail {
		textStyle := normalStyle
		switch {
		case d.bad:
			textStyle = errorStyle
		case d.dim:
			textStyle = dimStyle
		}
		b.WriteString("  ")
		b.WriteString(ruleStyle.Render("│"))
		b.WriteString(" ")
		if d.label != "" {
			b.WriteString(dimStyle.Render(fmt.Sprintf("%-12s", d.label)))
		}
		b.WriteString(textStyle.Render(ansi.Truncate(d.text, max(1, width-18), "…")))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	help := "  ↑/↓ choose   esc/q back"
	if cur.copyLabel != "" {
		help = "  ↑/↓ choose   enter copy " + cur.copyLabel + "   esc/q back"
	}
	if m.noticeFlash != "" {
		help += "   " + m.noticeFlash
	}
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	b.WriteString("\n")
	return b.String()
}

func (m launcherModel) updateNotices(msg tea.KeyMsg) (launcherModel, tea.Cmd) {
	m.noticeFlash = ""
	switch msg.String() {
	case "up", "k":
		if m.noticeCursor > 0 {
			m.noticeCursor--
		}
	case "down", "j":
		if m.noticeCursor < len(m.notices)-1 {
			m.noticeCursor++
		}
	case "esc", "q":
		m.noticesOpen = false
	case "enter":
		if m.noticeCursor < 0 || m.noticeCursor >= len(m.notices) {
			return m, nil
		}
		text := m.notices[m.noticeCursor].copyText
		if text == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			return noticeCopiedMsg{err: clipboard.WriteAll(text)}
		}
	}
	return m, nil
}

func parseNotice(text string, isErr bool, at time.Time) launcherNotice {
	text = strings.TrimSpace(text)
	n := launcherNotice{at: at, line: text, err: isErr}
	if text == "" {
		return n
	}
	if isProgressNotice(text) {
		n.progress = true
		n.line = text
		return n
	}
	switch {
	case strings.HasPrefix(text, "Meeting notes routed to "):
		return parseRouted(text, at)
	case strings.HasPrefix(text, "Meeting saved:"):
		return parseSaved(text, at)
	case strings.HasPrefix(text, "Meeting notes kept local only"):
		n.line = "Meeting notes kept on this Mac"
		if reason, ok := strings.CutPrefix(text, "Meeting notes kept local only"); ok {
			reason = strings.TrimSpace(reason)
			reason = strings.Trim(reason, "()")
			if reason != "" {
				n.detail = []noticeDetail{{text: reason, dim: true}}
			}
		}
	case strings.HasPrefix(text, "Meeting notes delegated to host"):
		n.line = "Meeting notes sent to the host"
		if _, detail, ok := strings.Cut(text, "("); ok {
			detail = strings.TrimSuffix(strings.TrimSpace(detail), ")")
			if detail != "" {
				n.detail = []noticeDetail{{text: detail}}
				if looksLikePath(detail) {
					n.copyText = detail
					n.copyLabel = "path"
				}
			}
		}
	case strings.HasPrefix(text, "Meeting route failed"):
		n.err = true
		n.line = "Meeting notes not filed"
		if _, detail, ok := strings.Cut(text, ":"); ok {
			detail = strings.TrimSpace(detail)
			if detail != "" {
				n.detail = []noticeDetail{
					{text: "Notes are still on this Mac."},
					{text: detail, bad: true},
				}
			}
		}
	case text == "Meeting route: mode=auto but no default destination":
		n.err = true
		n.line = "Meeting notes not filed"
		n.detail = []noticeDetail{{text: text, dim: true}}
	case strings.HasPrefix(text, "Meeting ended with error:"):
		return parseMeetingEnd(text, at)
	case strings.HasPrefix(text, "Speaker labels updated:"):
		return parseSpeakers(text, at)
	case strings.HasPrefix(text, "Speaker analysis failed:"):
		n.err = true
		n.line = "Speaker analysis failed"
		if _, detail, ok := strings.Cut(text, ":"); ok {
			detail = strings.TrimSpace(detail)
			if detail != "" {
				n.detail = []noticeDetail{{text: detail, bad: true}}
			}
		}
	case strings.HasPrefix(text, "Recovered pending meeting route"):
		rest := text
		if _, after, ok := strings.Cut(text, "—"); ok {
			rest = strings.TrimSpace(after)
		}
		inner := parseNotice(rest, false, at)
		inner.line = "Recovered a meeting"
		inner.at = at
		if inner.line == "Recovered a meeting" && rest != "" && len(inner.detail) == 0 && !strings.HasPrefix(rest, "Meeting notes routed") && !strings.HasPrefix(rest, "Meeting saved") {
			inner.detail = []noticeDetail{{text: rest, dim: true}}
		}
		return inner
	case strings.HasPrefix(text, "Recovered ") && strings.Contains(text, "pending meeting"):
		n.line = strings.Replace(text, "pending meeting routes", "meetings", 1)
		n.line = strings.Replace(n.line, "pending meeting route", "meeting", 1)
	case strings.HasPrefix(text, "Meeting route retry:"):
		n.err = true
		n.line = "Meeting recovery incomplete"
		n.detail = []noticeDetail{{text: text}}
	}
	return n
}

func isProgressNotice(text string) bool {
	switch {
	case strings.HasPrefix(text, "Discovering destinations"),
		strings.HasPrefix(text, "Routing meeting notes"),
		strings.HasPrefix(text, "Filing meeting notes"),
		strings.HasPrefix(text, "Filing to "):
		return strings.HasSuffix(text, "…") || strings.HasSuffix(text, "...")
	default:
		return false
	}
}

func parseRouted(text string, at time.Time) launcherNotice {
	rest := strings.TrimPrefix(text, "Meeting notes routed to ")
	id := rest
	detail := ""
	if before, after, ok := strings.Cut(rest, ": "); ok {
		id = strings.TrimSpace(before)
		detail = strings.TrimSpace(after)
	}
	n := launcherNotice{at: at, line: "Meeting notes filed to " + id}
	if detail == "" {
		return n
	}
	if strings.HasPrefix(detail, "{") {
		if doc, ok := parseImportDoc(detail); ok {
			return importNotice(n, doc)
		}
	}
	if looksLikePath(detail) {
		n.detail = []noticeDetail{{text: ellipsizePath(detail, 72)}}
		n.copyText = detail
		n.copyLabel = "path"
		return n
	}
	n.detail = []noticeDetail{{text: detail}}
	return n
}

type importDoc struct {
	CampaignRoot string `json:"campaign_root"`
	Note         struct {
		DurationSeconds float64 `json:"duration_seconds"`
		Path            string  `json:"path"`
		Transcript      string  `json:"transcript"`
		Speakers        int     `json:"speakers"`
		Utterances      int     `json:"utterances"`
		UpdatedExisting bool    `json:"updated_existing"`
	} `json:"note"`
}

func parseImportDoc(raw string) (importDoc, bool) {
	var doc importDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return importDoc{}, false
	}
	return doc, true
}

func importNotice(n launcherNotice, doc importDoc) launcherNotice {
	if doc.CampaignRoot != "" {
		n.detail = append(n.detail, noticeDetail{text: shortenHome(doc.CampaignRoot), dim: true})
	}
	notePath := joinCampaignPath(doc.CampaignRoot, doc.Note.Path)
	if notePath != "" {
		n.detail = append(n.detail, noticeDetail{label: "note", text: ellipsizePath(notePath, 72)})
		n.copyText = notePath
		n.copyLabel = "note"
	}
	transcript := joinCampaignPath(doc.CampaignRoot, doc.Note.Transcript)
	if transcript != "" {
		n.detail = append(n.detail, noticeDetail{label: "transcript", text: ellipsizePath(transcript, 72)})
	}
	if stats := importStats(doc); stats != "" {
		n.detail = append(n.detail, noticeDetail{text: stats, dim: true})
	}
	return n
}

func importStats(doc importDoc) string {
	var parts []string
	if doc.Note.Speakers > 0 {
		noun := "speakers"
		if doc.Note.Speakers == 1 {
			noun = "speaker"
		}
		parts = append(parts, fmt.Sprintf("%d %s", doc.Note.Speakers, noun))
	}
	if doc.Note.Utterances > 0 {
		noun := "utterances"
		if doc.Note.Utterances == 1 {
			noun = "utterance"
		}
		parts = append(parts, fmt.Sprintf("%d %s", doc.Note.Utterances, noun))
	}
	if sec := int(doc.Note.DurationSeconds); sec > 0 {
		parts = append(parts, formatRecorded(sec))
	}
	if len(parts) == 0 && doc.Note.Path == "" {
		return ""
	}
	if doc.Note.UpdatedExisting {
		parts = append(parts, "updated")
	} else {
		parts = append(parts, "new file")
	}
	return strings.Join(parts, " · ")
}

func formatRecorded(seconds int) string {
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("recorded %dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("recorded %dm %ds", m, s)
	default:
		return fmt.Sprintf("recorded %ds", s)
	}
}

func joinCampaignPath(root, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "~") {
		return rel
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return rel
	}
	return filepath.Join(root, rel)
}

func parseSaved(text string, at time.Time) launcherNotice {
	path := strings.TrimSpace(strings.TrimPrefix(text, "Meeting saved:"))
	n := launcherNotice{at: at, line: "Meeting saved"}
	if path == "" {
		return n
	}
	n.line = "Meeting saved · " + filepath.Base(path)
	n.detail = []noticeDetail{{text: ellipsizePath(path, 72)}}
	n.copyText = path
	n.copyLabel = "path"
	return n
}

func parseMeetingEnd(text string, at time.Time) launcherNotice {
	n := launcherNotice{at: at, err: true, line: "Meeting ended"}
	body := strings.TrimSpace(strings.TrimPrefix(text, "Meeting ended with error:"))
	cmd := ""
	if before, after, ok := strings.Cut(body, "recover with:"); ok {
		body = strings.TrimSpace(before)
		cmd = strings.TrimSpace(after)
	}
	dest := ""
	if before, after, ok := strings.Cut(body, "NOT filed to "); ok {
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(before), "—"))
		dest = after
		if before, _, ok := strings.Cut(dest, ";"); ok {
			dest = before
		}
		dest = strings.TrimSpace(dest)
	}
	if dest != "" {
		n.line = "Meeting ended · not filed to " + dest
	} else if body != "" {
		n.line = "Meeting ended · " + body
	}
	if body != "" {
		n.detail = append(n.detail, noticeDetail{text: body, bad: true})
	}
	if cmd != "" {
		n.detail = append(n.detail, noticeDetail{text: cmd})
		n.copyText = cmd
		n.copyLabel = "command"
	}
	return n
}

func parseSpeakers(text string, at time.Time) launcherNotice {
	n := launcherNotice{at: at, line: "Speakers labeled"}
	title, path, ok := splitNoticePath(text)
	count := ""
	if ok {
		title = strings.TrimPrefix(title, "Speaker labels updated:")
		title = strings.TrimSpace(title)
		if before, _, found := strings.Cut(title, " "); found {
			count = before
		}
	}
	if count != "" {
		n.line = "Speakers labeled · " + count
	}
	if path != "" {
		n.detail = []noticeDetail{{text: ellipsizePath(path, 72)}}
		n.copyText = path
		n.copyLabel = "path"
	}
	return n
}
