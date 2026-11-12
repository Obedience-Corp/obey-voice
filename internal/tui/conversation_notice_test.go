package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestLongNoticesWrapUnderTheirIndent(t *testing.T) {
	const long = "Hosted notice: Mira's replies come from Claude, and the conversation she sees leaves this machine whenever she answers."
	for _, tc := range []struct {
		name  string
		event events.Event
		text  string
	}{
		{"info", events.Info{Message: long}, long},
		{"error", events.Error{Message: long}, "Error: " + long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedConversation(t, 40, 40)
			m.handleEvent(tc.event)
			for _, width := range []int{40, 23} {
				m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				lines := noticeLines(t, m.viewport.View(), tc.text)
				if len(lines) < 3 {
					t.Fatalf("width %d: notice did not wrap: %q", width, lines)
				}
				for _, line := range lines {
					if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") || ansi.StringWidth(line) > width {
						t.Fatalf("width %d: line %q is not indented by two columns within the width:\n%s", width, line, strings.Join(lines, "\n"))
					}
				}
				if got := strings.Join(strings.Fields(strings.Join(lines, " ")), " "); got != tc.text {
					t.Fatalf("width %d: notice reads %q, want %q", width, got, tc.text)
				}
			}
		})
	}
}

// noticeLines returns the rendered lines from the one starting text's first
// word to the one ending its last, without styling or trailing padding.
func noticeLines(t *testing.T, view, text string) []string {
	t.Helper()
	words := strings.Fields(text)
	var out []string
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		line = strings.TrimRight(line, " ")
		if out == nil && !strings.Contains(line, words[0]) {
			continue
		}
		out = append(out, line)
		if strings.HasSuffix(line, words[len(words)-1]) {
			return out
		}
	}
	t.Fatalf("notice not found in view:\n%s", ansi.Strip(view))
	return nil
}
