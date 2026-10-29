package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type recordingOpenTerminal struct{ model conversationModel }

func (h *recordingOpenTerminal) Init() tea.Cmd { return tea.EnableMouseCellMotion }
func (h *recordingOpenTerminal) View() string  { return h.model.View() }
func (h *recordingOpenTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if _, ok := msg.(recordingOpenedMsg); ok {
		return h, tea.Quit
	}
	return h, cmd
}

func TestOpenRecordingThroughTerminalInput(t *testing.T) {
	for _, method := range []string{"mouse", "command"} {
		t.Run(method, func(t *testing.T) {
			m := sizedConversation(t, 100, 24)
			m.deps.runner = &fakeTurnRunner{}
			dir := filepath.Join(t.TempDir(), "Recording #1 café")
			m.lastRecordingDir = dir
			m.commandNotice("sealed talk · " + m.recordingLink(dir))
			opened := ""
			m.openRecording = func(_ context.Context, path string) error { opened = path; return nil }
			input := "/record open\r"
			if method == "mouse" {
				x, y := recordingCell(t, m, "Open recording")
				input = fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var output bytes.Buffer
			h := &recordingOpenTerminal{model: m}
			program := tea.NewProgram(h, tea.WithInput(strings.NewReader(input)), tea.WithOutput(&output), tea.WithContext(ctx), tea.WithoutSignalHandler())
			if _, err := program.Run(); err != nil {
				t.Fatal(err)
			}
			if opened != dir {
				t.Fatalf("terminal %s opened %q, want %q", method, opened, dir)
			}
			if !strings.Contains(ansi.Strip(output.String()), "Open recording") {
				t.Fatal("recording action did not reach terminal renderer")
			}
		})
	}
}

func recordingCell(t *testing.T, m conversationModel, label string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(line)
		if index := strings.Index(plain, label); index >= 0 {
			return ansi.StringWidth(plain[:index]), y
		}
	}
	t.Fatalf("missing %q in screen:\n%s", label, ansi.Strip(m.View()))
	return 0, 0
}

func TestRecordingClickFollowsRenderedLink(t *testing.T) {
	for _, width := range []int{32, 70, 120} {
		m := sizedConversation(t, width, 24)
		dir := filepath.Join(t.TempDir(), "录音 #1 ? café")
		m.commandNotice("录音 · sealed talk · " + m.recordingLink(dir))
		opened := ""
		m.openRecording = func(_ context.Context, path string) error { opened = path; return nil }
		x, y := recordingCell(t, m, "Open")
		if got := m.recordingHit(x, y); got != dir {
			t.Fatalf("width %d hit = %q, want %q", width, got, dir)
		}
		m, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if cmd == nil {
			t.Fatal("click did not open recording")
		}
		msg := cmd()
		if opened != dir {
			t.Fatalf("opened %q, want %q", opened, dir)
		}
		m, _ = m.Update(msg)
		if got := m.recordingHit(0, 0); got != "" {
			t.Fatalf("outside link opened %q", got)
		}
		m.activityFocused = true
		if got := m.recordingHit(x, y); got != "" {
			t.Fatalf("hidden chat link opened %q", got)
		}
	}
}

func TestRecordingLinksRespectScrollAndRetainTheirOwnTargets(t *testing.T) {
	m := sizedConversation(t, 100, 16)
	first, last := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "last")
	m.commandNotice("first " + m.recordingLink(first))
	for range 30 {
		m.commandNotice("spacer")
	}
	m.commandNotice("last " + m.recordingLink(last))
	m.viewport.GotoTop()
	x, y := recordingCell(t, m, "Open recording")
	if got := m.recordingHit(x, y); got != first {
		t.Fatalf("scrolled link = %q", got)
	}
	m.viewport.GotoBottom()
	x, y = recordingCell(t, m, "Open recording")
	if got := m.recordingHit(x, y); got != last {
		t.Fatalf("latest link = %q", got)
	}
}

func TestRecordOpenCommandAndFailure(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.configureRecord([]string{"off"})
	m.executeSlashCommand(slashCommand{id: commandRecord}, []string{"open"})
	if !strings.Contains(ansi.Strip(m.View()), "no recording yet") {
		t.Fatal("missing no-recording error")
	}
	m.lastRecordingDir = t.TempDir()
	m.openRecording = func(_ context.Context, path string) error {
		if path != m.lastRecordingDir {
			t.Fatalf("opened %q", path)
		}
		return errors.New("desktop opener unavailable")
	}
	cmd := m.executeSlashCommand(slashCommand{id: commandRecord}, []string{"open"})
	if cmd == nil {
		t.Fatal("missing open command")
	}
	// The command batches the opener with any microphone-resume work.
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		msg = batch[0]()
	}
	m, _ = m.Update(msg)
	if !strings.Contains(ansi.Strip(m.View()), "desktop opener unavailable") {
		t.Fatal("open failure was hidden")
	}
}
