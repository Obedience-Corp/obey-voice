package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestMenuCommandsLeaveChatLocally(t *testing.T) {
	for _, input := range []string{"/menu", "/back", "/men"} {
		for _, state := range []turnState{turnIdle, turnVoiceListening, turnVoiceResponding, turnTextRunning} {
			t.Run(fmt.Sprintf("%s/%d", input, state), func(t *testing.T) {
				runner := &fakeTurnRunner{}
				conv, _ := startedConversation(t, runner, false)
				conv.turnState = state
				conv, cmd := typeAndEnter(conv, input)
				if cmd == nil {
					t.Fatal("menu command did not leave chat")
				}
				msg := cmd()
				if _, ok := msg.(returnToMenuMsg); !ok {
					t.Fatalf("message = %T, want returnToMenuMsg", msg)
				}
				if conv.input.Value() != "" || len(runner.texts()) != 0 {
					t.Fatal("menu command was not consumed locally")
				}
				app := App{screen: screenConversation, conversation: conv}
				model, quit := app.Update(msg)
				app = model.(App)
				if !app.returnToMenu || app.quitting || app.screen != screenLauncher || quit == nil {
					t.Fatal("menu did not start an internal launcher transition")
				}
			})
		}
	}
}

func TestMenuSavesAndDrainsInsideOneProgram(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	runs, cleanups := 0, 0
	err := runPrograms(&config.Config{}, nil, nil, true, func(app App) (tea.Model, error) {
		runs++
		for i := 0; i < 2; i++ {
			previousCtx, previousSlot := app.runCtx, app.slot
			sess := session.New("ollama", "test")
			app.runtime = &ConversationRuntime{Cleanup: func() {
				cleanups++
				if err := sess.Save([]brain.Turn{{Role: "user", Content: fmt.Sprintf("Chat %d", i)}, {Role: "assistant", Content: "Saved reply"}}); err != nil {
					t.Error(err)
				}
			}}
			app.slot.store(app.runtime)
			model, _ := app.Update(returnToMenuMsg{})
			app = model.(App)
			if app.quitting || app.screen != screenLauncher {
				t.Fatal("menu exited the program")
			}
			<-app.menuDone
			model, _ = app.Update(menuReadyMsg{done: app.menuDone})
			app = model.(App)
			if previousCtx.Err() == nil || cleanups != i+1 || app.runtime != nil || app.slot == previousSlot {
				t.Fatal("menu did not isolate and drain the previous runtime")
			}
			saved, err := session.Load(sess.ID)
			if err != nil || len(saved.Turns) != 2 {
				t.Fatalf("session not saved: %+v %v", saved, err)
			}
			if len(app.launcher.items) == 0 || app.launcher.items[0].sessionID != sess.ID {
				t.Fatal("Continue does not offer saved chat")
			}
		}
		model, _ := app.Update(quitMsg{})
		return model, nil
	})
	if err != nil || runs != 1 || cleanups != 2 {
		t.Fatalf("err=%v runs=%d cleanups=%d", err, runs, cleanups)
	}
}

func TestMenuDoesNotRestartAfterProgramOrRuntimeError(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(fmt.Sprint(fatal), func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			want := errors.New("test failure")
			runs := 0
			err := runPrograms(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
				runs++
				if runs > 1 {
					t.Fatal("error reopened the launcher")
				}
				app.returnToMenu = true
				if fatal {
					app.fatalErr = want
					return app, nil
				}
				return app, want
			})
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestMenuIsDiscoverable(t *testing.T) {
	m := sizedConversation(t, 100, 30)
	m.input.SetValue("/men")
	m.syncComposer("")
	if !strings.Contains(stripANSI(m.View()), "/menu") {
		t.Fatal("command palette does not offer /menu")
	}
	m.input.SetValue("")
	m.syncComposer("/men")
	if !strings.Contains(stripANSI(m.View()), "/menu back") {
		t.Fatal("chat footer does not advertise /menu")
	}
}

func TestMenuShutdownReleasesConversationEventPump(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := newConversation("Test")
	m.deps.ctx = ctx
	m.bridge = newEventBridge(1)
	done := make(chan tea.Msg, 1)
	go func() { done <- m.rearm()() }()
	cancel()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatalf("canceled event pump returned %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("conversation event pump leaked after shutdown")
	}
}
