package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/app"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// handleSubmit routes an Enter press by turn state: idle submits; any in-flight
// voice or text turn is canceled first so typed text can barge in repeatedly
// (not only the first voice reply).
//
// Slash commands are local (never need the brain). They run immediately in
// any turn state so a long STT cancel or in-flight response cannot freeze
// /help, /settings, or unknown-command feedback.
func (m *conversationModel) handleSubmit() tea.Cmd {
	if m.deps.runner == nil {
		return nil
	}
	if m.media.importing || m.media.validating {
		return nil
	}
	if cmd, handled := m.mediaInputCommand(); handled {
		return cmd
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		if len(m.media.draft) == 0 {
			return nil
		}
		text = "Attached image."
	}
	text = expandPaletteSelection(text, m.commandSelection)

	// Local slash commands never require canceling the voice pipeline.
	if cmd, handled := m.submitLocalSlash(text); handled {
		return cmd
	}
	if m.selectionPending() {
		m.commandError("persona selection in progress")
		return nil
	}

	if cmd, handled := m.prepareMediaSubmit(text); handled {
		return cmd
	}
	switch m.turnState {
	case turnIdle:
		previous := m.input.Value()
		m.input.Reset()
		m.editor.sync("", 0)
		m.editor.resetUndo()
		m.syncComposer(previous)
		return m.submitText(text)
	case turnVoiceListening:
		// Prefer the synchronous cancel gate over turnState: UserInput can
		// already have been emitted (brain thinking) while the bridge has
		// not yet delivered it into handleEvent. After the gate closes,
		// turnState becomes turnVoiceResponding on the next UserInput event
		// and text barge-in is handled there instead.
		if m.canCancelVoice != nil && !m.canCancelVoice.Load() {
			// Transcript already final; treat as responding barge-in if we
			// still hold a turn cancel (brain/TTS may be in flight).
			if m.turnCancel != nil {
				return m.beginTextBargeIn(text)
			}
			return nil
		}
		return m.beginTextBargeIn(text)
	case turnVoiceResponding, turnTextRunning:
		// "Speaking — type to barge in" applies to both voice and text turns:
		// the first barge-in runs as text, so the reply is turnTextRunning —
		// Enter must cancel that turn too or barge-in only works once.
		return m.beginTextBargeIn(text)
	default:
		// Already canceling a prior barge-in; leave the draft until drain.
		return nil
	}
}

// beginTextBargeIn parks the draft, echoes it, and cancels the in-flight voice
// or text turn. handleVoiceTurnDone / handleTextTurnDone dispatch the text when
// the cancel drains.
func (m *conversationModel) beginTextBargeIn(text string) tea.Cmd {
	m.pendingInputRetry = false
	m.pendingText = text
	previous := m.input.Value()
	m.input.Reset()
	m.editor.sync("", 0)
	m.editor.resetUndo()
	m.syncComposer(previous)
	// Show the bubble immediately — cancel + text dispatch can take a
	// full STT/TTS shutdown, and clearing the composer otherwise looks like
	// the message was dropped.
	if !isNonChatSubmit(text) {
		m.echoUserTurn(text)
	}
	m.turnState = turnVoiceCanceling
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(false)
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	// Stop audible playback ASAP when the pipeline exposes a player stop.
	if m.deps.runner != nil {
		if stopper, ok := m.deps.runner.(interface{ StopPlayback() }); ok {
			stopper.StopPlayback()
		}
	}
	return nil
}

// keystrokeBargeIn silences TTS the moment the user types a printable rune
// while a response is in flight — the behavior the composer label promises
// ("type to barge in"). Unlike the Enter path, only playback stops: the turn
// keeps streaming so the reply text still lands, and the keystroke proceeds
// into the composer as the start of the user's next message. vim-normal and
// vim-visual keys never reach here, so transcript navigation stays silent.
func (m *conversationModel) keystrokeBargeIn(msg tea.KeyMsg) {
	if msg.Type != tea.KeyRunes || msg.Alt {
		return
	}
	if m.turnState != turnVoiceResponding && m.turnState != turnTextRunning {
		return
	}
	if m.deps.runner == nil {
		return
	}
	if stopper, ok := m.deps.runner.(interface{ StopPlayback() }); ok {
		stopper.StopPlayback()
	}
}

// discardDraft drops a barged-in draft that /compact beat to the cancel drain.
// The echo bubble stays as scrollback, but the pending-echo marker has to clear:
// it suppresses the next matching UserInput bubble, so leaving it set would
// swallow the user's re-send of the same text. The drop is announced because the
// draft was echoed and is never going to be answered.
func (m *conversationModel) discardDraft(text string) {
	m.restorePendingMedia()
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if m.pendingUserEcho == text {
		m.pendingUserEcho = ""
	}
	m.emit(events.Info{Message: "Compacting — the message you typed was not sent. Send it again once the summary lands."})
}

// expandPaletteSelection replaces an incomplete slash prefix with the
// highlighted palette match (e.g. "/sett" → "/settings") so Enter runs the
// selected command instead of reporting "Unknown command".
func expandPaletteSelection(text string, selection int) string {
	matches := matchingSlashCommands(text)
	if len(matches) == 0 {
		return text
	}
	token := commandToken(text)
	if token == "" {
		return text
	}
	if _, exact := commandForToken(token); exact {
		return text
	}
	selected := matches[min(selection, len(matches)-1)]
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) <= 1 {
		return selected.name
	}
	return selected.name + " " + strings.Join(fields[1:], " ")
}

// submitLocalSlash handles slash-command lines without touching turn state.
// handled is true for any line that starts with "/" (including unknown names).
func (m *conversationModel) submitLocalSlash(text string) (cmd tea.Cmd, handled bool) {
	command, args, found, slash := parseSlashCommand(text)
	if !slash {
		return nil, false
	}
	previous := m.input.Value()
	m.input.Reset()
	m.editor.sync("", 0)
	m.editor.resetUndo()
	m.syncComposer(previous)

	if !found {
		token := commandToken(text)
		msg := "Unknown command " + token + ". Type /help to list commands."
		if suggestion := suggestSlashCommand(token); suggestion != "" {
			msg = "Unknown command " + token + ". Did you mean " + suggestion + "? Type /help to list commands."
		}
		m.commandError(msg)
		// Leave voice/response turns alone — do not cancel or redispatch.
		return nil, true
	}
	return m.executeSlashCommand(command, args), true
}

// isNonChatSubmit reports slash commands and built-in control phrases that
// must not leave a user bubble in the transcript.
func isNonChatSubmit(text string) bool {
	if _, _, _, slash := parseSlashCommand(text); slash {
		return true
	}
	cmd := app.NormalizeCommand(text)
	return app.IsExitCommand(cmd) || app.IsClearCommand(cmd)
}

// submitText applies the command policy to typed input — commands never reach
// the brain, matching app.Run's text loop — then dispatches a text turn.
// Slash commands are normally handled earlier by submitLocalSlash; this path
// remains for the cancel-drain pendingText route.
func (m *conversationModel) submitText(text string) tea.Cmd {
	if cmd, handled := m.submitLocalSlash(text); handled {
		// If a slash was queued behind a voice cancel, restore listening when
		// the command itself does not navigate away or redispatch.
		if cmd != nil {
			return cmd
		}
		return m.reconcileBackground()
	}

	cmd := app.NormalizeCommand(text)
	switch {
	case app.IsExitCommand(cmd):
		m.quitting = true
		return quitCmd()

	case app.IsClearCommand(cmd):
		if m.selectionPending() {
			m.commandError("persona selection in progress")
			return m.resumeListening()
		}
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
		return m.resumeListening()

	}

	// Idle submit path: echo now. Cancel path already echoed in handleSubmit;
	// echoUserTurn is idempotent via pendingUserEcho on the later UserInput.
	if m.pendingUserEcho != text {
		m.echoUserTurn(text)
	}
	return m.dispatchTextTurn(text)
}

func (m *conversationModel) handleVoiceTurnDone(msg voiceTurnDoneMsg) tea.Cmd {
	m.turnCancel = nil
	m.pendingPause = false
	m.backgroundPauseGeneration++
	m.backgroundPauseAcknowledged = false
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(false)
	}
	m.keyOwnsTurn(false)
	wasCanceling := m.turnState == turnVoiceCanceling
	m.turnState = turnIdle

	if wasCanceling && m.pendingInputRetry {
		m.pendingInputRetry = false
		if m.selectionPending() {
			m.commandError("persona changed; send a new request")
			return m.resumeListening()
		}
		return m.dispatchInputRetry()
	}
	if wasCanceling {
		text := m.pendingText
		m.pendingText = ""
		talk := m.takeTalkTurn()
		if m.pendingCompact {
			if m.selectionPending() {
				m.pendingText = text
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			m.pendingCompact = false
			m.discardDraft(text)
			m.talkTurnEnded()
			return m.dispatchCompact()
		}
		if text != "" {
			if m.selectionPending() {
				m.pendingText = text
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			if m.voiceOn() && m.hasCapturedInput() {
				m.pendingText = text
				m.talkTurnEnded()
				return m.resumeListening()
			}
			m.talkTurnEnded()
			return m.submitText(text)
		}
		if talk {
			if m.selectionPending() {
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			// The key interrupted a reply and is still held: its turn opens
			// now, and the driver keeps owning it until the release.
			m.keyOwnsTurn(true)
			return m.dispatchVoiceTurn()
		}
		m.talkTurnEnded()
		return m.resumeListening()
	}
	m.talkTurnEnded()

	if msg.err != nil {
		if strings.TrimSpace(msg.text) != "" {
			// STT captured speech successfully; response-stage errors must
			// not inherit earlier input-failure strikes.
			m.voiceFailures = 0
		}
		switch app.ClassifyVoiceTurnFailure(msg.err, m.deps.ctx.Err(), msg.text, m.voiceFailures+1) {
		case app.VoiceShutdown:
			m.quitting = true
			return quitCmd()
		case app.VoiceFallback:
			m.voiceFailures = 0
			m.voiceEnabled = false
			m.emit(events.Error{Message: msg.err.Error()})
			m.emitAdmissionRetryNotice(msg.err, msg.text, msg.noticeEpoch)
			m.emit(events.Info{Message: "Voice input keeps failing — switching to text. Type /voice to switch back."})
			return m.resumeListening()
		default: // app.VoiceRetry
			if strings.TrimSpace(msg.text) == "" {
				m.voiceFailures++
			}
			m.emit(events.Error{Message: msg.err.Error()})
			m.emitAdmissionRetryNotice(msg.err, msg.text, msg.noticeEpoch)
			return tea.Tick(app.RetryBackoff, func(time.Time) tea.Msg { return voiceRetryMsg{} })
		}
	}

	m.voiceFailures = 0
	if msg.text == "" {
		return m.reconcileBackground() // silence — keep listening
	}

	// Voice commands match post-turn, identical to today: a spoken "goodbye"
	// has already received its spoken reply before the exit check runs.
	cmd := app.NormalizeCommand(msg.text)
	switch {
	case app.IsExitCommand(cmd):
		m.quitting = true
		return quitCmd()
	case app.IsClearCommand(cmd):
		if m.selectionPending() {
			m.commandError("persona selection in progress")
			return m.resumeListening()
		}
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
	}
	return m.reconcileBackground()
}

func (m *conversationModel) handleTextTurnDone(msg textTurnDoneMsg) tea.Cmd {
	m.turnCancel = nil
	m.keyOwnsTurn(false)
	// Barge-in during a text turn (agent speaking after a prior typed message)
	// sets turnVoiceCanceling + pendingText, then cancel drains here — same
	// path as voice barge-in via handleVoiceTurnDone.
	wasCanceling := m.turnState == turnVoiceCanceling
	pending := m.pendingText
	m.pendingText = ""
	m.turnState = turnIdle

	if wasCanceling && m.pendingInputRetry {
		m.pendingInputRetry = false
		if m.selectionPending() {
			m.commandError("persona changed; send a new request")
			return m.resumeListening()
		}
		return m.dispatchInputRetry()
	}
	if wasCanceling {
		talk := m.takeTalkTurn()
		if m.pendingCompact {
			if m.selectionPending() {
				m.pendingText = pending
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			m.pendingCompact = false
			m.discardDraft(pending)
			m.talkTurnEnded()
			return m.dispatchCompact()
		}
		if pending != "" {
			if m.selectionPending() {
				m.pendingText = pending
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			if m.voiceOn() && m.hasCapturedInput() {
				m.pendingText = pending
				m.talkTurnEnded()
				return m.resumeListening()
			}
			m.talkTurnEnded()
			return m.submitText(pending)
		}
		if talk {
			if m.selectionPending() {
				m.talkTurnEnded()
				return m.startNextPersonaSelection()
			}
			m.keyOwnsTurn(true)
			return m.dispatchVoiceTurn()
		}
		m.talkTurnEnded()
		return m.reconcileBackground()
	}
	m.talkTurnEnded()

	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.emit(events.Error{Message: msg.err.Error()})
		m.emitAdmissionRetryNotice(msg.err, msg.input, msg.noticeEpoch)
	}
	return m.reconcileBackground()
}

// requestCompact runs /compact when the pipeline is free, or cancels a
// cancelable in-flight voice turn and queues the compact for when the cancel
// drains. Compaction rebuilds the provider's context, so it never runs
// mid-turn; while a reply is in flight the user is asked to retry.
func (m *conversationModel) requestCompact() tea.Cmd {
	if m.selectionPending() {
		m.commandError("persona selection in progress")
		return nil
	}
	if m.deps.compact == nil {
		m.commandError("/compact is unavailable")
		return m.resumeListening()
	}
	switch m.turnState {
	case turnIdle:
		return m.dispatchCompact()
	case turnVoiceListening:
		if m.canCancelVoice != nil && m.canCancelVoice.Load() {
			m.pendingCompact = true
			m.turnState = turnVoiceCanceling
			m.canCancelVoice.Store(false)
			if m.turnCancel != nil {
				m.turnCancel()
			}
			return nil
		}
		m.commandError("busy — try /compact again after this turn")
		return nil
	case turnVoiceCanceling:
		m.pendingCompact = true
		return nil
	default: // turnVoiceResponding, turnTextRunning
		m.commandError("busy — try /compact again after this turn")
		return nil
	}
}

// dispatchCompact owns the pipeline for the compact operation the same way a
// text turn does, so a voice turn cannot start mid-compaction.
func (m *conversationModel) dispatchCompact() tea.Cmd {
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning
	m.setStatus("Compacting conversation", false)

	compact, wg := m.deps.compact, m.deps.wg
	return trackedCommand(m.deps.ctx, wg, func() tea.Msg {
		defer cancel()
		if ctx.Err() != nil {
			return compactDoneMsg{err: ctx.Err()}
		}
		return compactDoneMsg{err: compact(ctx)}
	})
}

func (m *conversationModel) handleCompactDone(msg compactDoneMsg) tea.Cmd {
	m.turnCancel = nil
	m.turnState = turnIdle
	m.setStatus("", false)
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.emit(events.Error{Stage: "compact", Message: msg.err.Error()})
	}
	return m.resumeListening()
}

func (m *conversationModel) handleVoiceRetry() tea.Cmd {
	if m.turnState == turnIdle && m.voiceOn() {
		return m.dispatchVoiceTurn()
	}
	return nil
}
