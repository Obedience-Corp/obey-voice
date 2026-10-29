package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTerminalPersonaCompletionAndMenuKeepsProgram(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	runner := &fakeTurnRunner{}
	conv, bus := startedConversation(t, runner, false)
	conv.mentions = mentionConversation(t).mentions
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conv.deps.ctx = ctx
	conv.deps.bus = bus
	conv.setSize(100, 30)
	cfg := &config.Config{AgentName: "Obey"}
	app := newApp(cfg, nil, nil)
	app.screen, app.width, app.height = screenConversation, 100, 30
	app.conversation = conv
	app.runCtx, app.runCancel, app.wg = ctx, cancel, &sync.WaitGroup{}
	app.slot, app.meetingSlot = newConversationSlot(), newMeetingSlot()
	sess := session.New("ollama", "fixture")
	var cleaned atomic.Bool
	app.runtime = &ConversationRuntime{Persist: func() {
		if err := sess.Save([]brain.Turn{{Role: "user", Content: "The saved fixture chat"}}); err != nil {
			t.Error(err)
		}
	}, Cleanup: func() { cleaned.Store(true) }}
	app.slot.store(app.runtime)
	h := &terminalPersonaApp{app: app, ready: make(chan struct{}), updates: make(chan terminalPersonaSnapshot, 128)}
	var terminal strings.Builder
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithAltScreen(), tea.WithoutSignalHandler())
	done := make(chan struct{})
	runErr := make(chan error, 1)
	go func() { _, err := program.Run(); runErr <- err; close(done) }()
	t.Cleanup(func() {
		program.Quit()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("terminal did not stop")
		}
	})
	<-h.ready
	program.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	sendTerminalText(program, "@jkr")
	awaitTerminalSnapshot(t, h.updates, "fuzzy persona palette", func(s terminalPersonaSnapshot) bool { return s.input == "@jkr" && strings.Contains(s.view, "@joker") })
	program.Send(tea.KeyMsg{Type: tea.KeyTab})
	awaitTerminalSnapshot(t, h.updates, "completed persona ID", func(s terminalPersonaSnapshot) bool { return s.input == "@joker " })
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlA})
	sendTerminalText(program, "/menu")
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	awaitTerminalSnapshot(t, h.updates, "launcher in same program", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenLauncher && !s.menuPending && strings.Contains(s.view, "Continue")
	})
	if !cleaned.Load() {
		t.Fatal("launcher became interactive before runtime cleanup")
	}
	saved, err := session.Load(sess.ID)
	if err != nil || len(saved.Turns) != 1 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	program.Quit()
	select {
	case <-done:
		if err := <-runErr; err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quit timed out")
	}
	if strings.Count(terminal.String(), "\x1b[?1049h") != 1 || strings.Count(terminal.String(), "\x1b[?1049l") != 1 {
		t.Fatal("menu left or re-entered alternate screen")
	}
}
