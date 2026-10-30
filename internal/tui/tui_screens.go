package tui

import (
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func (a App) onKey(msg tea.KeyMsg) (App, tea.Cmd, bool) {
	if msg.String() == "ctrl+c" && a.screen == screenConversation {
		// Selection in the composer still copies the draft first.
		if a.conversation.editor.selectionActive() {
			a.conversation.copySelection()
			return a, nil, true
		}
		// Idle chat (empty draft): first Ctrl+C yanks the last reply so the
		// mouse claim does not block copy; a second Ctrl+C within the
		// arming window falls through to quit (review: keep a non-slash exit).
		if a.conversation.composerIdle() &&
			(strings.TrimSpace(a.conversation.lastAssistantText) != "" ||
				strings.TrimSpace(a.conversation.streamingAgent) != "") {
			armed := !a.conversation.lastIdleCopyAt.IsZero() &&
				time.Since(a.conversation.lastIdleCopyAt) <= idleCopyQuitWindow
			if !armed {
				a.conversation.copyLastAssistant(true)
				return a, nil, true
			}
			// Second press within the window: continue to quit path below.
		}
	}
	// Meeting owns Ctrl+C as "stop recording" (returns to launcher).
	// Results and the route picker own it too: quitting the app from
	// those screens silently discarded the planned route (WI-162bbb).
	//
	// Not while the recorder is still being built: there is nothing to
	// stop, and the waiting screen tells the user ctrl+c quits — which it
	// must, or the only key it offers them does nothing.
	meetingOwnsCancel := (a.screen == screenMeeting && !a.meeting.startup.active) ||
		a.screen == screenMeetingSetup || a.screen == screenMeetingResults ||
		a.screen == screenMeetingRoute
	if msg.String() == "ctrl+c" && meetingOwnsCancel {
		return a, nil, false // fall through to screen Update
	}
	if msg.String() == "ctrl+c" {
		next, cmd := a.onQuit(quitMsg{})
		return next.(App), cmd, true
	}
	return a, nil, false
}

func (a App) onWindowSize(msg tea.WindowSizeMsg) App {
	a.width, a.height = msg.Width, msg.Height
	// Keep every screen's geometry current so switching into Sessions /
	// Settings / etc. does not render with height=0 (which capped the
	// sessions list at 3 rows).
	a.launcher, _ = a.launcher.Update(msg)
	a.settings, _ = a.settings.Update(msg)
	a.sessions, _ = a.sessions.Update(msg)
	a.meetingSetup, _ = a.meetingSetup.Update(msg)
	a.pickBook, _ = a.pickBook.Update(msg)
	a.library, _ = a.library.Update(msg)
	a.remote, _ = a.remote.Update(msg)
	a.personas, _ = a.personas.Update(msg)
	a.workers, _ = a.workers.Update(msg)
	if a.screen != screenConversation {
		a.conversation, _ = a.conversation.Update(msg)
	}
	// Meeting is updated via the fall-through delegate (typed tea.Model).
	return a
}

func (a App) onSwitchScreen(msg switchScreenMsg) (tea.Model, tea.Cmd) {
	target := screen(msg)
	if target == screenSettings && a.conversation.selectionPending() && (a.screen == screenConversation || a.screen == screenPersonas) {
		a.conversation.setStatus("Persona selection in progress", true)
		return a, nil
	}
	if a.screen == screenPickBook && target != screenPickBook {
		a.pickBook.cancelResolve()
	}
	if a.screen == screenLibrary && target != screenLibrary {
		a.library.cancelPrepare()
	}
	var pauseVoice tea.Cmd
	if target == screenWorkers && a.screen != screenWorkers {
		a.workersResumeVoice = a.screen == screenConversation && a.conversation.voiceEnabled
		if a.workersResumeVoice {
			pauseVoice = a.conversation.setInputMuted(true)
		}
	}
	if a.screen == screenWorkers && target != screenWorkers {
		if target == screenConversation && a.workersResumeVoice {
			pauseVoice = a.conversation.setInputMuted(false)
		}
		a.workersResumeVoice = false
	}
	if target == screenPersonas && a.screen != screenPersonas {
		a.personasResumeVoice = a.screen == screenConversation && a.conversation.voiceEnabled
		if a.personasResumeVoice {
			pauseVoice = a.conversation.setInputMuted(true)
		}
	}
	if target == screenSettings {
		a.settingsReturnScreen = a.screen
		a.settingsResumeVoice = a.screen == screenConversation && a.conversation.voiceEnabled
		a.settingsVoiceState = voiceSettingsState(a.cfg)
		if a.settingsReturnScreen == screenConversation {
			pauseVoice = a.conversation.setInputMuted(true)
		}
	}
	if a.screen == screenSettings {
		a.settings.closePreview()
		a.settings.cancelEnroll()
		a.settings.voices.reset()
	}
	if a.screen == screenPersonas && target != screenPersonas {
		a.personas.preview.close()
		a.personas.cancelBrainModels()
		if target == screenConversation && a.personasResumeVoice {
			pauseVoice = a.conversation.setInputMuted(false)
		}
		a.personasResumeVoice = false
	}
	if a.screen == screenRemote && target != screenRemote {
		a.remote.stop()
	}
	var leaveMeetingErr error
	if a.screen == screenMeeting && target != screenMeeting {
		leaveMeetingErr = a.stopMeetingRuntime()
	}
	prev := a.screen
	a.screen = target
	if leaveMeetingErr != nil && target == screenLauncher {
		a.launcher = a.launcher.withBanner(leaveMeetingErr.Error(), true)
	}
	switch a.screen {
	case screenSettings:
		// Replacing the model must not orphan an in-flight preview or player.
		a.settings.closePreview()
		a.settings.cancelEnroll()
		freshWorkers, workerLoadErr := config.LoadWithoutHooks()
		if workerLoadErr == nil && a.cfg != nil {
			a.cfg.VoiceWorkers = freshWorkers.VoiceWorkers.Clone()
		}
		a.settings = newSettings(a.cfg, a.providers)
		if workerLoadErr != nil {
			a.settings.message = "Could not refresh worker settings: " + workerLoadErr.Error()
		}
		if a.runtime != nil && a.runtime.Pipeline != nil && a.runtime.Pipeline.Agency != nil {
			agency := a.runtime.Pipeline.Agency
			a.settings.workerMigrationConfig = agency.WorkerMigrationConfig
		}
		// Only the App knows whether a conversation is running, so it owns
		// the choice between the live capture graph and a fresh one.
		a.settings.captureFor = a.settingsCaptureFor()
		a.settings.liveSeeder = a.settingsLiveSeeder()
		a.settings.onPlaybackVolume = a.applyPlaybackVolume
		a.settings.onWorkersEnabled = a.applyWorkersEnabled
		if a.runtime != nil && a.runtime.Pipeline != nil {
			a.settings.onDecisionSettings = a.runtime.Pipeline.ConfigureDecisions
			a.settings.decisionStatus = a.runtime.Pipeline.DecisionStatus
			a.settings.testDecisions = a.runtime.Pipeline.TestDecisions
		}
		// Apply stored geometry immediately — Bubble Tea only re-emits
		// WindowSize on actual resize, not on screen switches. Without
		// this, the list falls back to a default height and 80-col width
		// even in a large terminal split. Live resizes still flow through
		// WindowSizeMsg while settings is open.
		a.settings.resize(a.width, a.height)
		// Built before the return: the loaders mark a.settings as loading, and
		// that must be in the a that is returned.
		cmds := tea.Batch(a.settings.loadDevices(), a.settings.loadVoiceCatalog(), pauseVoice, a.settings.pollDecisionStatus())
		return a, cmds
	case screenWorkers:
		var controller workerController
		if a.runtime != nil && a.runtime.Pipeline != nil {
			controller = a.runtime.Pipeline
		}
		if demoWorkersEnabled() || demoWorkCueEnabled() {
			controller = demoWorkers(controller)
		}
		a.workers = newWorkers(a.cfg, controller)
		a.workers.returnScreen = prev
		a.workers.width, a.workers.height = a.width, a.height
		a.workers.refresh()
		return a, tea.Batch(a.workers.tick(), pauseVoice)
	case screenSessions:
		// Apply stored geometry immediately — Bubble Tea only re-emits
		// WindowSize on actual resize, not on screen switches.
		a.sessions.width, a.sessions.height = a.width, a.height
		a.sessions.ensureVisible()
	case screenMeetingSetup:
		a.meetingSetup = newMeetingSetup(a.cfg)
		a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
	case screenAudiobook:
		// Preserve form state when returning from the library picker.
		if prev != screenPickBook {
			a.audiobook = newAudiobook(a.cfg)
		}
	case screenPickBook:
		a.pickBook = newPickBook(a.cfg)
		a.pickBook.width, a.pickBook.height = a.width, a.height
		return a, a.pickBook.runBrowse()
	case screenLibrary:
		a.library = newLibrary(a.cfg)
		a.library.width, a.library.height = a.width, a.height
		return a, a.library.InitCmd()
	case screenRemote:
		a.remote = newRemote(a.runCtx, nil)
		a.remote.width, a.remote.height = a.width, a.height
		return a, a.remote.start()
	case screenPersonas:
		// Entering the screen replaces the model wholesale, so hand the
		// outgoing one's in-flight model scan its cancel instead of
		// dropping the func on the floor.
		a.personas.preview.close()
		a.personas.cancelBrainModels()
		a.personas.cancelVoiceCatalog()
		a.personas = newPersonas(a.cfg)
		a.personas.returnScreen = prev
		if prev == screenConversation && a.runtime != nil {
			if snapshot := a.runtime.SelectionSnapshot; snapshot != nil {
				a.personas.selectedPersona = func() string { return snapshot().PersonaID }
			} else {
				id := a.runtime.PersonaID
				a.personas.selectedPersona = func() string { return id }
			}
			a.personas.requestSelection = func(id string) tea.Cmd {
				return func() tea.Msg { return personaSelectionRequestMsg{id: id} }
			}
			if a.runtime.SelectionSnapshot != nil {
				a.conversation.applySelectionSnapshot(a.runtime.SelectionSnapshot())
			}
		}
		a.personas.width, a.personas.height = a.width, a.height
		a.personas.ensureVisible()
	case screenLauncher:
		// Refresh menu hints (active persona name) after Personas/Settings.
		if prev == screenPersonas || prev == screenSettings {
			saved := resumableSessions(session.List())
			a.launcher = newLauncher(a.cfg, a.providers, saved)
			a.launcher.width, a.launcher.height = a.width, a.height
		}
	}
	return a, pauseVoice
}

func (a App) onOpenSettingsSection(msg openSettingsSectionMsg) (tea.Model, tea.Cmd) {
	// Reuse the full settings-entry path (model rebuild, geometry, voice
	// pause), then land on the requested section.
	model, cmd := a.Update(switchScreenMsg(screenSettings))
	app := model.(App)
	// The same path a keypress takes, so Meeting's lazy destination load
	// runs when it is entered programmatically too.
	return app, tea.Batch(cmd, app.settings.gotoSection(msg.section))
}

func (a App) onBookPicked(msg bookPickedMsg) (tea.Model, tea.Cmd) {
	// Results only belong to the active picker screen. This prevents a late
	// conversion from changing the audiobook form after the user left, and
	// the request ID prevents an older result from winning after a reload.
	if a.screen != screenPickBook || msg.requestID != a.pickBook.requestID {
		return a, nil
	}
	a.pickBook.cancelResolve()
	if msg.err != nil {
		a.pickBook.errText = msg.err.Error()
		a.pickBook.message = ""
		return a, nil
	}
	a.pickBook.loadPhase = pickIdle
	a.audiobook.input = msg.path
	a.audiobook.errText = ""
	a.audiobook.message = "Filled input from Calibre library"
	a.audiobook.command = ""
	a.screen = screenAudiobook
	return a, nil
}

func (a App) onLibraryAudiobook(msg libraryAudiobookMsg) (tea.Model, tea.Cmd) {
	// Ignore stale prepare results (user left or started another book).
	if a.screen != screenLibrary || msg.requestID != a.library.requestID {
		return a, nil
	}
	a.library.cancelPrepare()
	if msg.err != nil {
		if a.library.pane == libPaneDetail {
			a.library.detailErr = msg.err.Error()
		} else {
			a.library.errText = msg.err.Error()
		}
		a.library.message = ""
		return a, nil
	}
	// Preserve a clean audiobook form, then fill the path from the library.
	a.audiobook = newAudiobook(a.cfg)
	a.audiobook.input = msg.path
	a.audiobook.errText = ""
	a.audiobook.message = "Filled input from Calibre library"
	a.audiobook.command = ""
	a.screen = screenAudiobook
	return a, nil
}

func (a App) onReturnToMenu(msg returnToMenuMsg) (tea.Model, tea.Cmd) {
	return a.beginMenuTransition()
}

func (a App) onQuit(msg quitMsg) (tea.Model, tea.Cmd) {
	a.returnToMenu = false
	if a.screen == screenPickBook {
		a.pickBook.cancelResolve()
	}
	if a.screen == screenLibrary {
		a.library.cancelPrepare()
	}
	a.settings.closePreview()
	a.personas.preview.close()
	a.settings.cancelEnroll()
	a.remote.stop()
	if err := a.stopMeetingRuntime(); err != nil {
		a.fatalErr = errors.Join(a.fatalErr, err)
	}
	a.conversation.cancelSelection()
	a.conversation.stopCapture()
	a.conversation.retireAdmissionRetryNotices()
	if a.conversation.turnCancel != nil {
		a.conversation.turnCancel()
	}
	// Ask, do not wait: this is the update loop.
	a.signalTalkHotkey()
	if a.runCancel != nil {
		a.runCancel()
	}
	a.quitting = true
	return a, tea.Quit
}

func (a App) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Conversation owns background voice/text turns. Deliver their async
	// completions even when the user is on Settings (or another screen) so
	// turnState cannot wedge on turnVoiceCanceling forever after /settings.
	if a.screen != screenConversation {
		switch msg.(type) {
		// Conversation ticks belong to the conversation even while hidden.
		// Dropping one leaves voiceTicking set but no timer scheduled.
		// Meeting animation uses the separate meetingTickMsg lifecycle.
		case mediaImported, mediaLoaded, mediaTick, mediaValidated, mediaOpenError, capturePollMsg, voiceTickMsg, voiceTurnDoneMsg, textTurnDoneMsg, voiceRetryMsg, busEventMsg,
			compactDoneMsg, backgroundReadyMsg, backgroundRecoveryMsg, backgroundPauseDoneMsg:
			var convCmd tea.Cmd
			a.conversation, convCmd = a.conversation.Update(msg)
			if eventMsg, ok := msg.(busEventMsg); ok {
				if event, ok := eventMsg.event.(events.PersonaSelection); ok {
					a.syncRuntimeSelection(event)
				}
			}
			// Still deliver the message to the active screen below; batch cmds.
			var cmd tea.Cmd
			switch a.screen {
			case screenLauncher:
				a.launcher, cmd = a.launcher.Update(msg)
			case screenSettings:
				a.settings, cmd = a.settings.Update(msg)
			case screenSessions:
				a.sessions, cmd = a.sessions.Update(msg)
			case screenMeetingSetup:
				a.meetingSetup, cmd = a.meetingSetup.Update(msg)
			case screenMeeting:
				var m tea.Model
				m, cmd = a.meeting.Update(msg)
				a.meeting = m.(meetingModel)
			case screenMeetingResults:
				a.meetingResults, cmd = a.meetingResults.Update(msg)
			case screenMeetingRoute:
				a.meetingRoute, cmd = a.meetingRoute.Update(msg)
			case screenAudiobook:
				a.audiobook, cmd = a.audiobook.Update(msg)
			case screenPickBook:
				a.pickBook, cmd = a.pickBook.Update(msg)
			case screenLibrary:
				a.library, cmd = a.library.Update(msg)
			case screenRemote:
				a.remote, cmd = a.remote.Update(msg)
			case screenPersonas:
				a.personas, cmd = a.personas.Update(msg)
			case screenWorkers:
				a.workers, cmd = a.workers.Update(msg)
			}
			return a, tea.Batch(convCmd, cmd)
		}
	}

	// Delegate to active screen.
	var cmd tea.Cmd
	switch a.screen {
	case screenLauncher:
		a.launcher, cmd = a.launcher.Update(msg)
	case screenSettings:
		a.settings, cmd = a.settings.Update(msg)
	case screenConversation:
		a.conversation, cmd = a.conversation.Update(msg)
		if eventMsg, ok := msg.(busEventMsg); ok {
			if event, ok := eventMsg.event.(events.PersonaSelection); ok {
				a.syncRuntimeSelection(event)
			}
		}
	case screenSessions:
		a.sessions, cmd = a.sessions.Update(msg)
	case screenMeetingSetup:
		a.meetingSetup, cmd = a.meetingSetup.Update(msg)
	case screenMeeting:
		var m tea.Model
		m, cmd = a.meeting.Update(msg)
		a.meeting = m.(meetingModel)
	case screenMeetingResults:
		a.meetingResults, cmd = a.meetingResults.Update(msg)
	case screenMeetingRoute:
		a.meetingRoute, cmd = a.meetingRoute.Update(msg)
	case screenAudiobook:
		a.audiobook, cmd = a.audiobook.Update(msg)
	case screenPickBook:
		a.pickBook, cmd = a.pickBook.Update(msg)
	case screenLibrary:
		a.library, cmd = a.library.Update(msg)
	case screenRemote:
		a.remote, cmd = a.remote.Update(msg)
	case screenPersonas:
		a.personas, cmd = a.personas.Update(msg)
	case screenWorkers:
		a.workers, cmd = a.workers.Update(msg)
	}

	return a, cmd
}
