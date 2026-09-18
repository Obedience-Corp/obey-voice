package tui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func formatNotice(text string, width int, isErr bool) string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 {
		return ""
	}
	style := statusStyle
	if isErr {
		style = errorStyle
	}
	if prefix, rest, found := splitJSONLead(text); found {
		title := prefix
		if title == "" {
			title = "Notice"
		}
		var body string
		if pretty, ok := indentJSON(ansi.Strip(rest)); ok {
			body = ansi.Hardwrap(colorJSON(pretty), max(1, width), true)
		} else {
			body = wrapWorkerPlain(rest, width)
		}
		return style.Render(compactWorkerText(title)) + "\n" + body
	}
	if title, path, ok := splitNoticePath(text); ok {
		base := filepath.Base(path)
		dir := ellipsizePath(filepath.Dir(path), max(1, width))
		var b strings.Builder
		b.WriteString(style.Render(compactWorkerText(title)))
		b.WriteByte('\n')
		b.WriteString(normalStyle.Render("  " + base))
		if dir != "" && dir != "." {
			b.WriteByte('\n')
			b.WriteString(dimStyle.Render("  " + dir))
		}
		return b.String()
	}
	return wrapStyled(style, text, width)
}

func formatNoticeCompact(text string, width int, isErr bool) string {
	text = strings.TrimSpace(text)
	if prefix, _, found := splitJSONLead(text); found && prefix != "" {
		text = prefix
	} else if title, path, ok := splitNoticePath(text); ok {
		text = title + " · " + filepath.Base(path)
	} else {
		text = shortenHome(text)
	}
	style := statusStyle
	if isErr {
		style = errorStyle
	}
	return ansi.Truncate(style.Render("  "+compactWorkerText(text)), max(1, width), "…")
}

func splitNoticePath(s string) (title, path string, ok bool) {
	i := strings.LastIndex(s, " · ")
	if i < 0 {
		return "", "", false
	}
	path = strings.TrimSpace(s[i+len(" · "):])
	if !looksLikePath(path) {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), path, true
}

func looksLikePath(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "./") {
		return true
	}
	return strings.ContainsRune(s, filepath.Separator) && !strings.ContainsFunc(s, unicode.IsSpace)
}

func shortenHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, home)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+len(home):]
		if rest == "" || strings.HasPrefix(rest, string(filepath.Separator)) {
			b.WriteByte('~')
			s = rest
			continue
		}
		b.WriteString(home)
		s = rest
	}
}

func ellipsizePath(path string, width int) string {
	path = shortenHome(filepath.Clean(path))
	if path == "." || path == string(filepath.Separator) {
		return ""
	}
	if lipgloss.Width(path) <= width {
		return path
	}
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) <= 2 {
		return ansi.Truncate(path, width, "…")
	}
	keep := 2
	for keep < len(parts) {
		candidate := parts[0] + string(filepath.Separator) + "…" + string(filepath.Separator) + strings.Join(parts[len(parts)-keep:], string(filepath.Separator))
		if parts[0] == "" {
			candidate = string(filepath.Separator) + "…" + string(filepath.Separator) + strings.Join(parts[len(parts)-keep:], string(filepath.Separator))
		}
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
		keep--
		if keep == 0 {
			break
		}
	}
	return ansi.Truncate(filepath.Base(path), width, "…")
}

func wrapStyled(style lipgloss.Style, text string, width int) string {
	return ansi.Hardwrap(style.Render(safeWorkerText(shortenHome(text))), max(1, width), true)
}
