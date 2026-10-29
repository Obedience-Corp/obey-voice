package tui

import (
	"context"
	"errors"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	tea "github.com/charmbracelet/bubbletea"
)

type menuReadyMsg struct {
	done       chan struct{}
	captureErr error
}

// Keep the terminal program mounted while the old conversation drains. No
// new runtime can start until its microphone, hotkey and workers are released.
func (a App) beginMenuTransition() (tea.Model, tea.Cmd) {
	if a.returnToMenu {
		return a, nil
	}
	model, _ := a.onQuit(quitMsg{})
	a = model.(App)
	a.quitting = false
	a.returnToMenu = true
	a.screen = screenLauncher
	a.launcher = a.launcher.withBanner("Saving conversation…", false)
	a.menuDone = make(chan struct{})
	old := a
	var captureErr error
	go func() {
		defer close(old.menuDone)
		captureErr = waitQuitHelpers(old, old, old.runCancel)
		if old.wg != nil {
			old.wg.Wait()
		}
		old.slot.persist()
		old.slot.cleanup()
		old.meetingSlot.cleanup()
	}()
	return a, func() tea.Msg {
		<-old.menuDone
		return menuReadyMsg{done: old.menuDone, captureErr: captureErr}
	}
}

func (a App) onMenuReady(msg menuReadyMsg) (tea.Model, tea.Cmd) {
	if !a.returnToMenu || a.quitting || a.menuDone != msg.done {
		return a, nil
	}
	next := newApp(a.cfg, a.providers, session.List())
	next.conversation.capturePending = a.conversation.capturePending
	next.conversation.captureSealStarted = a.conversation.captureSealStarted
	next.conversation.captureSealWarned = a.conversation.captureSealWarned
	next.fatalErr = errors.Join(a.fatalErr, msg.captureErr)
	if msg.captureErr != nil {
		next.launcher = next.launcher.withBanner(msg.captureErr.Error(), true)
	}
	next.builder, next.meetingBuilder = a.builder, a.meetingBuilder
	next.runCtx, next.runCancel = context.WithCancel(context.Background())
	next.wg = &sync.WaitGroup{}
	next.progress = newEventBridge(16)
	next.slot, next.meetingSlot = newConversationSlot(), newMeetingSlot()
	next = next.onWindowSize(tea.WindowSizeMsg{Width: a.width, Height: a.height})
	return next, next.conversation.capturePollCmd()
}
