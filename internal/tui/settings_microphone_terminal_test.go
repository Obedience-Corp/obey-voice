package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

type microphoneTerminalHarness struct {
	app      App
	keys     chan string
	switched chan string
	mu       sync.Mutex
	output   bytes.Buffer
}

func (h *microphoneTerminalHarness) Init() tea.Cmd { return nil }
func (h *microphoneTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := h.app.Update(msg)
	h.app = model.(App)
	if _, ok := msg.(tea.KeyMsg); ok {
		h.keys <- h.app.View()
	}
	if _, ok := msg.(microphoneSwitchedMsg); ok {
		h.switched <- h.app.View()
	}
	return h, cmd
}
func (h *microphoneTerminalHarness) View() string { return h.app.View() }
func (h *microphoneTerminalHarness) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.output.Write(p)
}

func TestMicrophoneSelectionThroughTerminalPersistsAndRebinds(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	opened := make(chan string, 1)
	app := microphoneSettingsApp(t, func(_ context.Context, name string) error { opened <- name; return nil })
	app.settings.saveConfig = nil // exercise the real config writer in the isolated directory
	app.settings.section = sectionAudio
	app.settings.inputItems = []string{"", "Wireless Mic", "Studio Display Microphone"}
	app.settings.width, app.settings.height = 120, 40
	app.width, app.height = 120, 40
	app.conversation, _ = app.conversation.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for i := range app.settings.sectionLines() {
		app.settings.cursor = i
		row, line, _ := app.settings.focused()
		if row.key == "input_device" && line.option == 2 {
			break
		}
	}
	h := &microphoneTerminalHarness{app: app, keys: make(chan string, 2), switched: make(chan string, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	defer func() { program.Quit(); <-done }()
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case view := <-h.keys:
		if !strings.Contains(stripANSI(view), "Studio Display Microphone") {
			t.Fatalf("picker did not select display mic:\n%s", view)
		}
	case <-ctx.Done():
		t.Fatal("picker timed out")
	}
	program.Send(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case view := <-h.switched:
		if !strings.Contains(stripANSI(view), "Microphone switched to") {
			t.Fatalf("conversation did not confirm live selection:\n%s", view)
		}
	case <-ctx.Done():
		t.Fatal("live rebind timed out")
	}
	if name := <-opened; name != "Studio Display Microphone" {
		t.Fatalf("opened %q", name)
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InputDevice != "Studio Display Microphone" {
		t.Fatalf("persisted input = %q", loaded.InputDevice)
	}
}
