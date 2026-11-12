package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const pathNotice = "Saved to /Users/someone/.obey/agents/voice/obey-voice/recordings/2026-11-11T09-30-00Z\nOpen it from Finder."

func TestLongNoticesWrapUnderTheirIndent(t *testing.T) {
	const long = "Hosted notice: Mira's replies come from Claude, and the conversation she sees leaves this machine whenever she answers."
	for _, tc := range []struct {
		name  string
		event events.Event
		text  string
	}{
		{"info", events.Info{Message: long}, long},
		{"error", events.Error{Message: long}, "Error: " + long},
		{"unbroken path", events.Info{Message: pathNotice}, pathNotice},
		{"cjk", events.Info{Message: "通知：ミラの返答はクラウドから届き、この会話はミラが答えるたびにこの端末の外へ送られます。"}, "通知：ミラの返答はクラウドから届き、この会話はミラが答えるたびにこの端末の外へ送られます。"},
		{"embedded ansi", events.Info{Message: "Model \x1b[31mhaiku\x1b[0m is not available on this account; choose another model in Settings and try again."}, "Model haiku is not available on this account; choose another model in Settings and try again."},
		{"tabs", events.Info{Message: "Paths:\t/one/two\tthree four five six seven eight nine ten eleven twelve"}, "Paths: /one/two three four five six seven eight nine ten eleven twelve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedConversation(t, 40, 40)
			m.handleEvent(tc.event)
			for _, width := range []int{40, 23} {
				m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				raw := noticeLines(t, m.viewport.View(), tc.text)
				if len(raw) < 3 {
					t.Fatalf("width %d: notice did not wrap: %q", width, raw)
				}
				lines := make([]string, len(raw))
				for i, line := range raw {
					if strings.ContainsAny(ansi.Strip(line), "\t\x1b") || strings.Contains(line, "\x1b[31m") {
						t.Fatalf("width %d: line %q kept a tab or the notice's own escape", width, line)
					}
					lines[i] = strings.TrimRight(ansi.Strip(line), " ")
					if !strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "   ") || ansi.StringWidth(lines[i]) > width {
						t.Fatalf("width %d: line %q is not indented by two columns within the width:\n%s", width, lines[i], strings.Join(lines, "\n"))
					}
				}
				// A token longer than the line is split, so compare without whitespace.
				if got, want := squash(strings.Join(lines, "")), squash(tc.text); got != want {
					t.Fatalf("width %d: notice reads %q, want %q", width, got, want)
				}
			}
		})
	}
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

func TestNarrowNoticesFitDoubleWidthText(t *testing.T) {
	item := chatItem{kind: chatItemSys, sys: "fallback", hang: "通知です"}
	if got := item.render(false, 3); got != "fallback" {
		t.Fatalf("width 3 rendered %q, want the unwrapped fallback", got)
	}
	for _, line := range strings.Split(ansi.Strip(item.render(false, 4)), "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") || ansi.StringWidth(line) > 4 {
			t.Fatalf("width 4 line %q does not fit under the indent", line)
		}
	}
}

// noticeLines returns the rendered lines, styling kept, from the one holding
// the start of text to the one ending it.
func noticeLines(t *testing.T, view, text string) []string {
	t.Helper()
	plain := []rune(squash(text))
	head, tail := string(plain[:4]), string(plain[len(plain)-4:])
	var out []string
	for _, line := range strings.Split(view, "\n") {
		visible := squash(ansi.Strip(line))
		if out == nil && !strings.HasPrefix(visible, head) {
			continue
		}
		out = append(out, line)
		if strings.HasSuffix(visible, tail) {
			return out
		}
	}
	t.Fatalf("notice not found in view:\n%s", ansi.Strip(view))
	return nil
}
