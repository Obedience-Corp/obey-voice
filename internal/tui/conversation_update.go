package tui

import (
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
)

func voiceTickCmd() tea.Cmd {
	return tea.Tick(voiceTickInterval, func(t time.Time) tea.Msg { return voiceTickMsg(t) })
}

func (m conversationModel) Update(msg tea.Msg) (next conversationModel, cmd tea.Cmd) {
	if cmd, handled := m.updateMedia(msg); handled {
		return m, cmd
	}

	// Schedule only after the returned state has captured its current scope and
	// deadline, including snapshots recovered while voice animation is idle.
	defer func() {
		if next.capture != nil {
			next.capture.SetInputEnabled(next.voiceEnabled)
		}
		cmd = tea.Batch(cmd, next.capturePollCmd())
		if expiry := next.scheduleWorkStripExpiry(); expiry != nil {
			cmd = tea.Batch(cmd, expiry)
		}
	}()
	switch msg := msg.(type) {
	case recordingOpenedMsg:
		if msg.err != nil {
			m.commandError("open recording: " + msg.err.Error())
		}
		return m, nil
	case capturePollMsg:
		m.capturePollScheduled = false
		m.pollCapture()
		return m, nil
	case tea.WindowSizeMsg:
		m.graphics.measure()
		m.setSize(msg.Width, msg.Height)
		return m, tea.Batch(m.ensureVoiceTick(), m.mediaCommand())
	case tea.FocusMsg:
		m.recoverWork(time.Now(), true)
		return m, nil
	case workProjectionPollMsg:
		if msg.generation != m.workPollGeneration {
			return m, nil
		}
		m.recoverWork(time.Now(), false)
		return m, tea.Batch(m.workProjectionPollCmd(), m.ensureVoiceTick())

	case startupTickMsg:
		if !m.startup.tick() {
			return m, nil
		}
		return m, startupTickCmd(m.reducedMotion)

	case voiceTickMsg:
		m.recoverWork(time.Time(msg), false)
		m.voiceFrame++
		m.advanceContextMeter()
		// Decay mic energy so the meter settles when speech pauses.
		m.inputLevel *= 0.82
		if m.inputLevel < 0.02 {
			m.inputLevel = 0
		}
		m.outputLevel *= 0.88
		if m.outputLevel < 0.02 {
			m.outputLevel = 0
		}
		if !m.shouldAnimateVoice() {
			m.voiceTicking = false
			return m, nil
		}
		return m, voiceTickCmd()

	case demoVoiceAnimStartedMsg, demoThinkStartedMsg, demoWorkCueStartedMsg:
		return m, m.ensureVoiceTick()

	case busEventMsg:
		if msg.source != nil && msg.source != m.bridge {
			return m, nil
		}
		m.handleEvent(msg.event)
		return m, tea.Batch(m.rearm(), m.ensureVoiceTick(), m.mediaCommand())

	case workStripExpiryMsg:
		if msg.scope != m.workDisplay.snapshot.Scope ||
			msg.deadline != m.workDisplay.terminalUntil ||
			time.Now().Before(msg.deadline) {
			return m, nil
		}
		m.reflowAt(msg.deadline)
		return m, nil

	case backgroundReadyMsg:
		// Listening remains owned by STT until its reply or no-speech timeout;
		// canceling it here could discard an utterance that is just beginning.
		m.pendingBackground = true
		return m, tea.Batch(m.resumeListening(), m.waitBackground())
	case backgroundRecoveryMsg:
		return m, tea.Batch(m.reconcileBackground(), m.backgroundRecoveryCmd())
	case backgroundPauseDoneMsg:
		if msg.generation != m.backgroundPauseGeneration {
			return m, nil
		}
		m.pendingPause = false
		if msg.err == nil && msg.acknowledged {
			m.backgroundPauseAcknowledged = true
		}
		return m, nil
	case voiceTurnDoneMsg:
		if msg.lifetime != nil && msg.lifetime != m.deps.ctx {
			return m, nil
		}
		return m, tea.Batch(m.handleVoiceTurnDone(msg), m.ensureVoiceTick())

	case textTurnDoneMsg:
		if msg.lifetime != nil && msg.lifetime != m.deps.ctx {
			return m, nil
		}
		return m, tea.Batch(m.handleTextTurnDone(msg), m.ensureVoiceTick())

	case recordingDoneMsg:
		return m, tea.Batch(m.finishRecording(msg), m.ensureVoiceTick())

	case compactDoneMsg:
		return m, tea.Batch(m.handleCompactDone(msg), m.ensureVoiceTick())

	case voiceRetryMsg:
		return m, tea.Batch(m.handleVoiceRetry(), m.ensureVoiceTick())

	case talkHotkeyEventMsg:
		return m, tea.Batch(m.handleTalkHotkey(msg.ev), m.ensureVoiceTick())

	case talkHotkeyExitedMsg:
		m.talkExited()
		return m, nil

	case liveSpeakerStatsMsg:
		m.liveSpeakerStats = msg.stats
		m.liveSpeakerStatsKnown = true
		return m, liveSpeakerStatsCmd(m.liveSpeaker)

	case mediaOpenError:
		if msg.err != nil {
			m.commandError(msg.err.Error())
		}
		return m, nil
	case clipboardPasteMsg:
		if msg.err != nil {
			m.commandError("paste failed: " + msg.err.Error())
			return m, nil
		}
		if len(msg.image) > 0 {
			return m, m.importMedia(nil, msg.image)
		}
		if paths, ok := chatmedia.Paths(msg.text); ok {
			return m, m.importMedia(paths, nil)
		}
		m.insertClipboardText(msg.text)
		return m, nil

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			if dir := m.recordingHit(msg.X, msg.Y); dir != "" {
				return m, m.openRecordingCmd(dir)
			}
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && m.workCueHit(msg.X, msg.Y) {
			id := m.workerShortcutID()
			return m, func() tea.Msg { return openWorkerMsg{jobID: id} }
		}
		// The viewport already implements wheel acceleration and bounds. Route
		// only vertical wheel events here so clicks and drags do not interfere
		// with the always-focused composer.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			// Strip shift: the viewport reads it as horizontal scroll, which is
			// a no-op at the default horizontal step. Terminals that use shift
			// as the selection modifier would otherwise give the user neither
			// selection nor scrolling.
			msg.Shift = false
			return m.updateScroll(msg)
		}
		// A drag that reaches here selected nothing: the terminal handed the
		// mouse to the app. Say once how to select and copy.
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionMotion && !m.selectHintShown {
			m.selectHintShown = true
			m.commandNotice(selectHint)
		}
		return m, nil

	case tea.KeyMsg:
		if absorbed, scroll, ok := m.absorbMouseFragment(msg); ok {
			return absorbed, scroll
		} else {
			m = absorbed
		}
		if m.startup.active {
			// Nothing is wired yet: a turn typed here would be dispatched at a
			// nil runner. ctrl+c still quits (the app handles it above).
			return m, nil
		}
		if msg.Paste {
			if paths, ok := chatmedia.Paths(string(msg.Runes)); ok {
				return m, m.importMedia(paths, nil)
			}
		}
		m.syncEditorFromTextarea()
		// Page keys scroll history. Editing keys stay with the always-focused
		// composer so multiline drafting never needs a mode switch.
		switch msg.String() {
		case "f2":
			m.media.paused = !m.media.paused
			m.refreshContent()
			return m, m.mediaCommand()
		case "f3":
			return m, m.openMedia()
		case "f4":
			m.removeMedia("")
			return m, nil
		case "ctrl+v", "ctrl+shift+v", "shift+insert":
			return m, readClipboard(m.clipboard())
		case "ctrl+y":
			// Always available: yank last assistant reply without fighting quit.
			m.copyLastAssistant()
			return m, nil
		case "ctrl+a":
			m.selectAll()
			return m, nil
		case "ctrl+x":
			if m.editor.selectionActive() {
				m.cutSelection()
				return m, nil
			}
		case "ctrl+g":
			return m, m.toggleInputMuted()
		case "ctrl+o":
			m.toggleOutputMuted()
			return m, nil
		case "ctrl+t":
			if m.workActivity {
				m.workActivity = false
				m.workFilter = ""
			}
			m.activityFocused = !m.activityFocused
			m.refreshActivity()
			return m, nil
		case "ctrl+w":
			id := m.workerShortcutID()
			return m, func() tea.Msg { return openWorkerMsg{jobID: id} }
		case "ctrl+k":
			m.toggleShowThink()
			return m, nil
		case "ctrl+r":
			if !m.vim.enabled {
				m.toggleRecordTalk()
				return m, nil
			}
		case "esc":
			if m.vim.mode == vimVisual {
				m.enterVimNormal()
				return m, nil
			}
			if m.editor.selectionActive() {
				m.editor.clearSelection()
				return m, nil
			}
			if m.activityFocused && m.workFilter != "" {
				m.workFilter = ""
				m.refreshActivity()
				return m, nil
			}
			if m.activityFocused {
				m.activityFocused = false
				m.workActivity = false
				m.refreshActivity()
				return m, nil
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			return m.updateScroll(msg)
		case "ctrl+home":
			m.activeViewport().GotoTop()
			m.syncFollowFromViewports()
			return m, nil
		case "ctrl+end":
			m.activeViewport().GotoBottom()
			m.syncFollowFromViewports()
			return m, nil
		case "home":
			// Activity always jumps; Chat only when the composer is empty so
			// bare Home/End still navigate the transcript without fighting
			// line-start/end editing while drafting.
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoTop()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "end":
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoBottom()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "up", "down":
			// Same rule Home/End already follow: an empty composer scrolls the
			// transcript, a draft moves the cursor.
			//
			// This is what keeps the wheel usable now that Samantha leaves the
			// mouse to the terminal. In the alternate screen a terminal with no
			// mouse claim translates wheel turns into arrow keys, and an idle
			// composer is exactly the moment a wheel turn means "scroll the
			// chat" rather than "move my cursor".
			if m.activityFocused || m.input.Value() == "" {
				return m.updateScroll(msg)
			}
		}

		if handled, cmd := m.handleCommandPaletteKey(msg); handled {
			return m, cmd
		}
		if msg.String() == "enter" {
			return m, m.handleSubmit()
		}
		// Do not bind bare "y" for yank: empty-composer "y" steals the first
		// letter of ordinary messages (yes/you/yeah). Use Ctrl+Y, /copy, or
		// idle Ctrl+C instead. Vim NORMAL keeps its own y/yy operators below.
		if m.vim.enabled {
			switch m.vim.mode {
			case vimNormal:
				return m, m.handleVimNormalKey(msg)
			case vimVisual:
				return m, m.handleVimVisualKey(msg)
			case vimInsert:
				if msg.String() == "esc" {
					m.enterVimNormal()
					return m, nil
				}
			}
		}
		// Reaching here means the key is going into the composer as typed
		// text — the moment "type to barge in" promises silence.
		m.keystrokeBargeIn(msg)
		return m, m.updateComposer(msg)

	default:
		// Non-key messages (notably textarea.Blink ticks) must reach the
		// composer so the cursor blink chain stays alive.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *conversationModel) shouldAnimateVoice() bool {
	if m.reducedMotion {
		return false
	}
	running, _ := m.activeWorkerCounts()
	return m.voiceMode != anim.ModeIdle || running > 0 || m.compactStage != "" || m.contextFill != m.contextTarget()
}

// ensureVoiceTick starts the animation loop once while voice or background work is active.
func (m *conversationModel) ensureVoiceTick() tea.Cmd {
	if !m.shouldAnimateVoice() || m.voiceTicking {
		return nil
	}
	m.voiceTicking = true
	return voiceTickCmd()
}

// rearm re-issues the bridge drain Cmd; it must follow every consumed
// busEventMsg or the model stops receiving bus events.
func (m conversationModel) rearm() tea.Cmd {
	if m.bridge == nil {
		return nil
	}
	return m.bridge.waitContext(m.deps.ctx)
}
