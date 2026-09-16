package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestQuitCancelsRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := App{cfg: &config.Config{}, runCtx: ctx, runCancel: cancel, width: 80, height: 24}
	start := time.Now()
	next, cmd := app.Update(quitMsg{})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("quit took %s on the update loop, want it to ask and return", elapsed)
	}
	if ctx.Err() == nil {
		t.Fatal("quit did not cancel the run context")
	}
	got := next.(App)
	if !got.quitting {
		t.Fatal("quit did not mark the app quitting")
	}
	if cmd == nil {
		t.Fatal("quit scheduled nothing, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not return tea.Quit")
	}
}

func TestQuitPersistsSessionBeforeCleanup(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var order []string
	err := runPrograms(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
		app.slot.store(&ConversationRuntime{
			Persist: func() { order = append(order, "persist") },
			Cleanup: func() { order = append(order, "cleanup") },
		})
		model, _ := app.Update(quitMsg{})
		return model, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "persist,cleanup" {
		t.Fatalf("teardown order = %v, want persist then cleanup", order)
	}
}

func TestProcessQuitForceExitsWedgedCleanup(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var persisted atomic.Bool
	var exited atomic.Int32
	oldExit, oldAfter := processForceExit, processForceQuitAfter
	processForceExit = func(code int) { exited.Store(int32(code)) }
	processForceQuitAfter = 40 * time.Millisecond
	t.Cleanup(func() {
		processForceExit = oldExit
		processForceQuitAfter = oldAfter
	})

	err := runPrograms(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
		app.slot.store(&ConversationRuntime{
			Persist: func() { persisted.Store(true) },
			Cleanup: func() { time.Sleep(250 * time.Millisecond) },
		})
		model, _ := app.Update(quitMsg{})
		return model, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Load() {
		t.Fatal("quit did not persist the session before wedged cleanup")
	}
	deadline := time.Now().Add(time.Second)
	for exited.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := exited.Load(); got != 130 {
		t.Fatalf("force exit = %d, want 130", got)
	}
}
