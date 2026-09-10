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
				if !app.returnToMenu || !app.quitting || quit == nil {
					t.Fatal("menu did not request session teardown and launcher restart")
				}
			})
		}
	}
}

func TestMenuSavesAndDrainsBeforeReopeningLauncher(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var previousCtx context.Context
	var previousSession *session.Session
	var previousSlot *runtimeSlot[*ConversationRuntime]
	cleanups, runs := 0, 0
	err := runPrograms(&config.Config{}, nil, nil, true, func(app App) (tea.Model, error) {
		runs++
		if runs > 3 {
			t.Fatal("quit reopened the launcher")
		}
		if runs > 1 {
			if previousCtx.Err() == nil || cleanups != runs-1 {
				t.Fatal("launcher reopened before canceling and cleaning up the old session")
			}
			if app.startInConversation || app.screen != screenLauncher || app.runtime != nil || app.slot == previousSlot {
				t.Fatal("launcher inherited the old conversation lifetime")
			}
			if len(app.launcher.items) == 0 || app.launcher.items[0].sessionID != previousSession.ID {
				t.Fatal("Continue does not offer the chat just saved")
			}
			saved, err := session.Load(previousSession.ID)
			if err != nil || len(saved.Turns) != 2 || saved.Turns[1].Content != "Saved reply" {
				t.Fatalf("chat history was not persisted: session=%+v err=%v", saved, err)
			}
		} else if !app.startInConversation {
			t.Fatal("initial resume did not start in conversation")
		}
		if runs == 3 {
			model, _ := app.Update(quitMsg{})
			return model, nil
		}
		previousCtx, previousSlot = app.runCtx, app.slot
		sess := session.New("ollama", "test")
		previousSession = sess
		drained := make(chan struct{})
		app.wg.Add(1)
		go func() {
			defer app.wg.Done()
			<-app.runCtx.Done()
			close(drained)
		}()
		app.runtime = &ConversationRuntime{Cleanup: func() {
			select {
			case <-drained:
			default:
				t.Error("runtime cleanup ran before the turn drained")
			}
			cleanups++
			if err := sess.Save([]brain.Turn{{Role: "user", Content: fmt.Sprintf("Chat %d", runs)}, {Role: "assistant", Content: "Saved reply"}}); err != nil {
				t.Error(err)
			}
		}}
		app.slot.store(app.runtime)
		model, _ := app.Update(returnToMenuMsg{})
		return model, nil
	})
	if err != nil || runs != 3 || cleanups != 2 {
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
