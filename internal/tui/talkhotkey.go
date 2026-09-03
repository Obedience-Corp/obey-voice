package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// talkHotkeyEventMsg carries one line from the talk-hotkey helper.
type talkHotkeyEventMsg struct {
	ev talkhotkey.Event
}

// talkHotkeyExitedMsg reports the helper process ending; its event stream is
// closed and no further keys will arrive.
type talkHotkeyExitedMsg struct{}

// startTalkHotkey spawns the Darwin helper for the conversation that just
// became ready. It is a no-op when the key is disabled, on other platforms, or
// when the session has no microphone. A helper that cannot start reports why
// in the conversation instead of failing the session: Ctrl+G still works.
func (a *App) startTalkHotkey() tea.Cmd {
	if a.hotkey != nil || a.runtime == nil || !a.runtime.Voice {
		return nil
	}
	if !a.cfg.TalkHotkeyEnabled || !talkhotkey.Supported() {
		return nil
	}
	combo, err := config.TalkHotkeyComboOf(a.cfg)
	if err != nil {
		a.conversation.talkUnavailable(err.Error())
		return nil
	}
	// macOS delivers one hot key to every process that registered it, so
	// sharing the combo with the Mac app would open a turn in both. Leave it
	// to the app and say so; a different combo for the TUI is one setting.
	if owner, ok := talkhotkey.OtherOwner(combo); ok {
		a.conversation.talkUnavailable(owner + " is running and answers " + combo.Display() + " itself; set talk_hotkey to another combo for the TUI")
		return nil
	}
	client, err := talkhotkey.Start(a.runCtx, combo, talkhotkey.Options{})
	if err != nil {
		a.conversation.talkUnavailable(fmt.Sprintf("helper did not start: %v", err))
		return nil
	}
	a.hotkey = client
	a.conversation.attachTalk(combo, config.TalkHotkeyModeOf(a.cfg))
	return a.nextTalkHotkeyEvent()
}

// nextTalkHotkeyEvent is the re-arming Cmd that pumps the helper's events into
// the program, whichever screen is showing.
func (a *App) nextTalkHotkeyEvent() tea.Cmd {
	client := a.hotkey
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-client.Events()
		if !ok {
			return talkHotkeyExitedMsg{}
		}
		return talkHotkeyEventMsg{ev: ev}
	}
}

// stopTalkHotkey ends the helper. Idempotent.
func (a *App) stopTalkHotkey() {
	if a.hotkey == nil {
		return
	}
	a.hotkey.Stop()
	a.hotkey = nil
}

// talkState is the conversation's view of the talk key: which combo, which
// mode, and whether the helper actually holds the key right now.
type talkState struct {
	driver     *talkhotkey.Driver
	combo      talkhotkey.Combo
	registered bool
	// pendingTurn queues a hotkey turn behind a cancel that is draining: the
	// key interrupted a reply, and the turn opens once the cancel lands.
	pendingTurn bool
}

func (m *conversationModel) attachTalk(combo talkhotkey.Combo, mode talkhotkey.Mode) {
	m.talk = &talkState{driver: talkhotkey.NewDriver(mode), combo: combo}
}

// talkUnavailable records why there is no talk key this session.
func (m *conversationModel) talkUnavailable(reason string) {
	m.appendActivity("talk key", "unavailable", 0)
	m.emit(events.Info{Message: "Talk hotkey unavailable: " + reason + ". Ctrl+G still toggles the mic."})
}

// talkHint is the footer fragment for a registered key, empty otherwise.
func (m conversationModel) talkHint() string {
	if m.talk == nil || !m.talk.registered {
		return ""
	}
	return m.talk.combo.Display() + " talk"
}

// handleTalkHotkey applies one helper event. Registration and conflicts are
// reported; key transitions go through the mode driver and become turn
// actions.
func (m *conversationModel) handleTalkHotkey(ev talkhotkey.Event) tea.Cmd {
	if m.talk == nil {
		return nil
	}
	switch ev.Kind {
	case talkhotkey.KindRegistered:
		m.talk.registered = true
		m.appendActivity("talk key", m.talk.combo.Display()+" "+string(m.talk.driver.Mode()), 0)
		return nil
	case talkhotkey.KindError:
		m.talk.registered = false
		m.talk.driver.Reset()
		m.appendActivity("talk key", "conflict", 0)
		if ev.Conflict() {
			m.setStatus(m.talk.combo.Display()+" is taken by another app — rebind in Settings → Tools", true)
		} else {
			m.setStatus("Talk hotkey failed: "+ev.Detail, true)
		}
		m.emit(events.Info{Message: "Talk hotkey " + m.talk.combo.Display() + " unavailable: " + ev.Detail + " Ctrl+G still toggles the mic."})
		return nil
	case talkhotkey.KindDown:
		return m.applyTalkAction(m.talk.driver.Down())
	case talkhotkey.KindUp:
		return m.applyTalkAction(m.talk.driver.Up(ev.Held()))
	}
	return nil
}

// talkExited clears the key once the helper is gone; the driver's turn
// bookkeeping ends with it.
func (m *conversationModel) talkExited() {
	if m.talk == nil {
		return
	}
	if m.talk.registered {
		m.appendActivity("talk key", "stopped", 0)
	}
	m.talk.registered = false
	m.talk.driver.Reset()
}

func (m *conversationModel) applyTalkAction(action talkhotkey.Action) tea.Cmd {
	switch action {
	case talkhotkey.ActionBegin:
		return m.beginTalkTurn()
	case talkhotkey.ActionEnd:
		return m.endTalkTurn()
	}
	return nil
}

// beginTalkTurn opens a spoken turn for the key. The mute stays as it is: a
// muted session gets exactly one bounded turn and is still muted afterwards,
// because resumeListening never redispatches while voice input is off. A key
// pressed over a reply interrupts it first, the way a Mac hotkey press does.
func (m *conversationModel) beginTalkTurn() tea.Cmd {
	if m.deps.runner == nil || !m.deps.voice {
		m.setStatus("Microphone unavailable", true)
		m.talk.driver.TurnEnded()
		return nil
	}
	switch m.turnState {
	case turnIdle:
		return m.dispatchVoiceTurn()
	case turnVoiceListening:
		if m.canCancelVoice != nil && m.canCancelVoice.Load() {
			// The mic is already open on this turn; the key adopts it and
			// its release will commit it.
			return nil
		}
		return m.interruptForTalk()
	case turnVoiceResponding, turnTextRunning:
		return m.interruptForTalk()
	default:
		m.talk.pendingTurn = true
		return nil
	}
}

// interruptForTalk cancels whatever owns the pipeline and queues the key's
// turn behind the cancel drain. Playback stops now; the turn opens when the
// done message lands.
func (m *conversationModel) interruptForTalk() tea.Cmd {
	m.talk.pendingTurn = true
	m.turnState = turnVoiceCanceling
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(false)
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	if stopper, ok := m.deps.runner.(interface{ StopPlayback() }); ok {
		stopper.StopPlayback()
	}
	return nil
}

// endTalkTurn commits the utterance the key opened. Nothing is second-guessed:
// a release with nothing said still ends the turn, and the pipeline reports an
// empty transcript. A release after the recogniser already finished is a
// no-op; a release before an interrupt drained simply drops the queued turn.
func (m *conversationModel) endTalkTurn() tea.Cmd {
	m.talk.pendingTurn = false
	if m.turnState != turnVoiceListening {
		return nil
	}
	if m.canCancelVoice != nil && !m.canCancelVoice.Load() {
		return nil
	}
	if m.deps.endUtterance != nil {
		m.deps.endUtterance()
	}
	return nil
}

// talkTurnEnded tells the driver a turn closed on its own, so a key still held
// does not end a turn that is already over, and a toggle starts fresh.
func (m *conversationModel) talkTurnEnded() {
	if m.talk != nil {
		m.talk.driver.TurnEnded()
	}
}

// takeTalkTurn reports and clears a hotkey turn queued behind a cancel.
func (m *conversationModel) takeTalkTurn() bool {
	if m.talk == nil || !m.talk.pendingTurn {
		return false
	}
	m.talk.pendingTurn = false
	return true
}
