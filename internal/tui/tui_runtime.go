package tui

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func (a App) onWorkerSettingsSaved(msg workerSettingsSavedMsg) (tea.Model, tea.Cmd) {
	if a.runtime != nil && a.runtime.Pipeline != nil && a.runtime.Pipeline.Agency != nil {
		if err := a.runtime.Pipeline.Agency.ApplyWorkerSettings(msg.workers); err != nil {
			a.settings.message = "Worker settings saved; runtime update failed: " + err.Error()
		}
	}
	return a, nil
}

func (a App) onSettingsDone(msg settingsDoneMsg) (tea.Model, tea.Cmd) {
	a.settings.closePreview()
	a.settings.cancelEnroll()
	target := a.settingsReturnScreen
	if target == screenSettings {
		target = screenLauncher
	}
	a.screen = target
	resumeVoice := target == screenConversation && a.settingsResumeVoice
	a.settingsResumeVoice = false
	// Unstick cancel/response state if turn-done msgs were dropped while
	// Settings was active (conversation is not the focused screen).
	a.conversation.recoverTurnState()
	voiceChanged := a.settingsVoiceState != voiceSettingsState(a.cfg)
	a.settingsVoiceState = ""
	if target == screenConversation && a.runtime != nil && a.runtime.Voice && a.cfg != nil &&
		a.cfg.InputDevice != a.runtime.InputDevice {
		a.conversation.setStatus("Switching microphone…", false)
		return a, switchMicrophone(a.runCtx, a.runtime, a.cfg.InputDevice, resumeVoice, voiceChanged)
	}
	if target == screenConversation && voiceChanged && a.runtime != nil && a.runtime.ReloadVoice != nil {
		a.conversation.setStatus("Applying voice settings…", false)
		return a, reloadVoice(a.runCtx, a.runtime.ReloadVoice, resumeVoice)
	}
	if resumeVoice {
		return a, a.conversation.setInputMuted(false)
	}
	return a, nil
}

func (a App) onMicrophoneSwitched(msg microphoneSwitchedMsg) (tea.Model, tea.Cmd) {
	if a.runtime != msg.runtime {
		return a, nil
	}
	if msg.err != nil {
		a.conversation.setStatus("Microphone change failed: "+msg.err.Error()+". Voice input remains paused.", true)
		return a, nil
	}
	a.runtime.InputDevice = msg.name
	if a.runtime.SessionCfg != nil {
		updated := *a.runtime.SessionCfg
		updated.InputDevice = msg.name
		a.runtime.SessionCfg = &updated
		a.conversation.cfg = &updated
	}
	a.conversation.inputDevice = msg.name
	a.conversation.deps.inputDevice = msg.name
	a.conversation.appendActivity("input", deviceLabel(msg.name), 0)
	if msg.reloadVoice && a.runtime.ReloadVoice != nil {
		return a, reloadVoice(a.runCtx, a.runtime.ReloadVoice, msg.resumeVoice)
	}
	a.conversation.setStatus("Microphone switched to "+deviceLabel(msg.name), false)
	if msg.resumeVoice {
		return a, a.conversation.setInputMuted(false)
	}
	return a, nil
}

func (a App) onVoiceReloaded(msg voiceReloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.conversation.setStatus("Voice settings: "+msg.err.Error(), true)
	} else {
		// ReloadVoice re-resolves the session persona binding. Prefer that
		// snapshot for the badge so a global active_persona cannot relabel
		// an in-flight uncle-fu conversation as kokoro/af_heart.
		if a.runtime != nil && a.runtime.SessionCfg != nil {
			a.conversation.cfg = a.runtime.SessionCfg
		} else {
			a.conversation.cfg = a.cfg
		}
		// Settings edits the active persona profile. When the open session
		// is a different persona, ReloadVoice intentionally keeps the
		// session voice — do not claim the conversation took Settings.
		status := "Voice settings applied"
		if a.runtime != nil && a.runtime.PersonaID != "" && a.cfg != nil &&
			a.cfg.ActivePersona != "" && a.runtime.PersonaID != a.cfg.ActivePersona {
			status = "Session voice preserved (settings apply to active persona / next sessions)"
		}
		a.conversation.setStatus(status, false)
	}
	if msg.resumeVoice {
		return a, a.conversation.setInputMuted(false)
	}
	return a, nil
}

func (a App) onStartPipeline(msg startPipelineMsg) (tea.Model, tea.Cmd) {
	a.settings.closePreview()
	a.settings.cancelEnroll()
	if a.builder == nil {
		// No runtime wiring (tests): nothing to build.
		return a, nil
	}
	a.screen = screenConversation
	a.startingSessionID = msg.sessionID
	// The build takes seconds to minutes; the screen says so until it lands.
	tick := a.conversation.beginStartup(msg.personaName)
	return a, tea.Batch(tick, a.progress.wait(),
		buildRuntime(a.builder, a.runCtx, a.progress, a.slot, msg.sessionID, msg.personaID))
}

func (a App) onBuildStage(msg buildStageMsg) (tea.Model, tea.Cmd) {
	// Whichever screen is waiting owns the stage; only one build runs at a
	// time, and the meeting screen is entered before its build starts.
	if a.screen == screenMeeting {
		a.meeting.setBuildStage(msg.stage)
	} else {
		a.conversation.setBuildStage(msg.stage)
	}
	return a, a.progress.wait()
}

func (a App) onProgressClosed(msg progressClosedMsg) (tea.Model, tea.Cmd) {
	return a, nil
}

func (a App) onRuntimeReady(msg runtimeReadyMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		var bindingErr *PersonaBindingError
		if !errors.As(msg.err, &bindingErr) {
			var bindingValue PersonaBindingError
			if errors.As(msg.err, &bindingValue) {
				bindingErr = &bindingValue
			}
		}
		if bindingErr != nil {
			// A bad/deleted persona is recoverable before the runtime has
			// opened capture. Return to the chooser and let the next valid
			// selection start the normal conversation build.
			a.fatalErr = nil
			a.screen = screenPersonas
			a.personas = newPersonas(a.cfg)
			a.personas.returnScreen = screenLauncher
			a.personas.message = bindingErr.Error()
			sessionID := a.startingSessionID
			if bindingErr.SessionID != "" {
				sessionID = bindingErr.SessionID
			}
			a.personas.startPersona = func(id, name string) tea.Cmd {
				return func() tea.Msg {
					return startPipelineMsg{sessionID: sessionID, personaID: id, personaName: name}
				}
			}
			a.personas.width, a.personas.height = a.width, a.height
			a.personas.ensureVisible()
			return a, nil
		}
		a.fatalErr = msg.err
		return a.onQuit(quitMsg{})
	}
	// Build finished after quit: slot already cleaned (or will be in run).
	if a.quitting || a.runCtx.Err() != nil {
		return a, nil
	}
	a.runtime = msg.rt
	// The session's identity comes from its binding, resolved at build:
	// a later persona switch must not relabel this conversation.
	if msg.rt.AgentName != "" {
		a.conversation.agentName = msg.rt.AgentName
	}
	// TTS badge / chrome must mirror the pipeline voice for this session,
	// not the still-active global persona (e.g. uncle-fu vs samantha).
	if msg.rt.SessionCfg != nil {
		a.conversation.cfg = msg.rt.SessionCfg
	}
	a.conversation.finishStartup()
	a.conversation.setStatus("", false)
	a.conversation.seedTranscript(msg.rt.Seed)
	cmd := a.conversation.startConversation(conversationDeps{
		runner: msg.rt.Pipeline,
		bus:    msg.rt.Bus,
		clearHistory: func() {
			if err := msg.rt.Pipeline.ClearConversation(); err != nil {
				msg.rt.Bus.Emit(events.Error{Stage: "work", Message: err.Error()})
				return
			}
			if msg.rt.Pipeline.OnTurn != nil {
				msg.rt.Pipeline.OnTurn()
			}
		},
		workSnapshot:  func() work.Snapshot { return msg.rt.Pipeline.WorkSnapshot() },
		journalHealth: msg.rt.Pipeline.JournalHealth,
		workScope:     func() work.WorkScope { return msg.rt.Pipeline.WorkSnapshot().Scope },
		workersSummary: func() string {
			running, queued := 0, 0
			for _, item := range msg.rt.Pipeline.Workers() {
				if item.State == "queued" {
					queued++
				} else if item.Active() {
					running++
				}
			}
			return workersCountLabel(running, queued, msg.rt.Pipeline.WorkersEnabled())
		},
		brainSession:   brainSessionReporter(msg.rt.Pipeline.Brain),
		compact:        msg.rt.Pipeline.CompactConversation,
		voice:          msg.rt.Voice,
		output:         msg.rt.Output,
		setOutputMuted: msg.rt.Pipeline.SetOutputMuted,
		sessionID:      msg.rt.SessionID,
		inputDevice:    msg.rt.InputDevice,
		outputDevice:   msg.rt.OutputDevice,
		liveSpeaker:    msg.rt.LiveSpeaker,
		speakerNames:   msg.rt.SpeakerNames,
		endUtterance:   msg.rt.Pipeline.EndUtterance,
		ctx:            a.runCtx,
		wg:             a.wg,
	})
	// Seed the visible projection from durable/live coordinator state. This
	// is display-only; restored results remain held and are never spoken or
	// acknowledged merely because the TUI started.
	a.conversation.handleEvent(events.WorkStateChanged{Snapshot: msg.rt.Pipeline.WorkSnapshot()})
	a.conversation.configureSelection(msg.rt)
	if msg.rt.SelectionSnapshot != nil {
		a.conversation.applySelectionSnapshot(msg.rt.SelectionSnapshot())
	}
	a.installNameSummon(msg.rt)
	return a, tea.Batch(cmd, a.startTalkHotkey())
}
