package tui

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestMenuWaitsForTurnAndRejectsRetiredMessages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wg := &sync.WaitGroup{}
	wg.Add(1)
	app := newApp(&config.Config{}, nil, nil)
	app.runCtx, app.runCancel, app.wg = ctx, cancel, wg
	app.slot, app.meetingSlot = newConversationSlot(), newMeetingSlot()
	oldSlot := app.slot
	oldBridge := newEventBridge(8)
	app.conversation.bridge = oldBridge
	model, _ := app.Update(returnToMenuMsg{})
	app = model.(App)
	select {
	case <-app.menuDone:
		t.Fatal("menu finished before active turn drained")
	default:
	}
	wg.Done()
	<-app.menuDone
	model, _ = app.Update(menuReadyMsg{done: app.menuDone})
	app = model.(App)
	app.conversation.deps.ctx = app.runCtx
	app.conversation.turnState = turnTextRunning
	for _, msg := range []any{
		runtimeReadyMsg{owner: oldSlot, err: errors.New("old startup failed")},
		busEventMsg{source: oldBridge, event: events.UserInput{Text: "stale input"}},
		textTurnDoneMsg{lifetime: ctx, input: "old request"},
		talkHotkeyExitedMsg{lifetime: ctx},
	} {
		model, _ = app.Update(msg)
		app = model.(App)
	}
	if app.fatalErr != nil || app.quitting || app.conversation.turnState != turnTextRunning || len(app.conversation.items) != 0 {
		t.Fatal("old conversation mutated its replacement")
	}
	app.runCancel()
}
