package tui

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Keep native terminal hyperlinks and handle reported clicks ourselves when
// mouse reporting is enabled. The command also works in terminals without OSC 8.
func (m *conversationModel) recordingLink(dir string) string {
	path, err := filepath.Abs(dir)
	if err != nil {
		return "Open recording (unavailable)"
	}
	target := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	if m.recordingTargets == nil {
		m.recordingTargets = make(map[string]string)
	}
	m.recordingTargets[target] = path
	label := lipgloss.NewStyle().Foreground(colorAccent).Underline(true).Render("Open recording")
	return ansi.SetHyperlink(target) + label + ansi.ResetHyperlink() + " · /record open"
}

// Measure the rendered screen, including wrapping, scroll position and panels.
// Only targets registered by recording notices can launch a local application.
func (m conversationModel) recordingHit(x, y int) string {
	if x < 0 || y < 0 || x >= m.width || y >= m.height || len(m.recordingTargets) == 0 {
		return ""
	}
	view := m.View()
	var state byte
	var col, row int
	var target string
	for len(view) > 0 && row <= y {
		seq, width, n, next := ansi.DecodeSequence(view, state, nil)
		view, state = view[n:], next
		if strings.HasPrefix(seq, "\x1b]8;") {
			payload := strings.TrimSuffix(strings.TrimSuffix(seq[4:], "\x1b\\"), "\a")
			_, target, _ = strings.Cut(payload, ";")
		}
		if seq == "\n" {
			row++
			col = 0
			continue
		}
		if row == y && x >= col && x < col+width {
			return m.recordingTargets[target]
		}
		col += width
	}
	return ""
}

type recordingOpenedMsg struct{ err error }

func (m *conversationModel) openRecordingCmd(dir string) tea.Cmd {
	if dir == "" {
		m.commandError("no recording yet — /record talk or /record lab")
		return nil
	}
	open := m.openRecording
	if open == nil {
		open = openRecordingDirectory
	}
	ctx := m.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return recordingOpenedMsg{err: open(ctx, dir)}
	}
}

func openRecordingDirectory(ctx context.Context, dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", dir)
	case "windows":
		cmd = exec.CommandContext(ctx, "explorer.exe", dir)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", dir)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not open %s: %w", dir, err)
	}
	return nil
}
