package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

// This opt-in harness runs the real conversation model in a real terminal with
// isolated config. Its synthetic admission failure is UI/lifecycle evidence,
// never voice/provider/performance evidence.
func TestRecordTerminalHarness(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_CAPTURE_TERMINAL_ROOT")
	if root == "" {
		t.Skip("opt-in terminal harness")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("terminal root must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	config.SetConfigDirForTest(t, root)
	m, bus := startedConversation(t, &fakeTurnRunner{}, false)
	m.sessionID = "ov0007-terminal"
	m.setSize(110, 32)
	model := recordTerminalModel{m: m, bus: bus}
	program := tea.NewProgram(model, tea.WithAltScreen())

	result, err := program.Run()
	if err != nil {
		t.Fatal(err)
	}
	final := result.(recordTerminalModel)
	final.m.stopCapture()
	if final.m.capturePending != nil {
		select {
		case <-final.m.capturePending.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("terminal capture did not seal")
		}
	}
}

type recordTerminalModel struct {
	m   conversationModel
	bus *events.Bus
}

func (m recordTerminalModel) Init() tea.Cmd { return tea.Batch(m.m.rearm(), m.m.ensureVoiceTick()) }
func (m recordTerminalModel) View() string  { return m.m.View() }
func (m recordTerminalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	previous := m.m.capture
	next, cmd := m.m.Update(msg)
	if previous == nil && next.capture != nil {
		m.bus.Emit(events.Error{Stage: "admit", Message: "Synthetic admission error: terminal capture verification"})
	}
	m.m = next
	return m, cmd
}
