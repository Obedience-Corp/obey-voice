package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

func (a App) onPersonaSelectionRequest(msg personaSelectionRequestMsg) (tea.Model, tea.Cmd) {
	if a.screen != screenPersonas || a.personas.returnScreen != screenConversation {
		return a, nil
	}
	cmd := a.conversation.enqueuePersonaSelection(msg.id)
	if cmd == nil && (a.conversation.selectionRuntime == nil || a.conversation.selectionRuntime.SelectPersona == nil) {
		a.personas.message = "Persona switching is unavailable for this conversation"
	} else if a.conversation.selectionBusy || len(a.conversation.selectionQueue) > 1 {
		a.personas.message = "Persona selection queued"
	} else {
		a.personas.message = "Preparing persona…"
	}
	return a, cmd
}

func (a App) onPersonaSelectionResult(msg personaSelectionResultMsg) (tea.Model, tea.Cmd) {
	// Selection publication belongs to this update thread. A failed
	// preparation leaves the old runtime/config/display untouched.
	accepted, next := a.conversation.finishPersonaSelection(msg)
	if !accepted {
		return a, nil
	}
	if msg.err != nil {
		a.personas.message = fmt.Sprintf("Failed to switch persona: %v", msg.err)
	} else {
		name := msg.selection.DisplayName
		if name == "" {
			name = msg.selection.PersonaID
		}
		a.personas.message = fmt.Sprintf("Active persona: %s", name)
	}
	if msg.err == nil {
		if msg.cfg != nil {
			a.conversation.cfg = msg.cfg
			if a.runtime != nil {
				a.runtime.SessionCfg = msg.cfg
			}
		}
		if a.runtime != nil {
			if msg.selection.PersonaID != "" {
				a.runtime.PersonaID = msg.selection.PersonaID
			}
			if msg.selection.DisplayName != "" {
				a.runtime.AgentName = msg.selection.DisplayName
			}
		}
		a.conversation.applySelectionSnapshot(msg.selection)
		// The shared admission route owns name addressing for a live
		// session. Keep the independent frontend/self-echo protection,
		// but do not leave the old per-turn gate installed.
		if a.runtime != nil && a.runtime.Pipeline != nil && a.runtime.Pipeline.AdmitInput != nil {
			a.runtime.Pipeline.AddressGate = nil
		}
	}
	if next == nil && a.screen == screenConversation {
		next = a.conversation.resumeListening()
	}
	return a, next
}

func (a App) onTalkHotkeyEvent(msg talkHotkeyEventMsg) (tea.Model, tea.Cmd) {
	if msg.lifetime != nil && msg.lifetime != a.runCtx {
		return a, nil
	}
	// Browsing personas or the workers manager pauses voice input. Keep
	// draining the global key without allowing a press to reopen the mic
	// behind either screen.
	if (a.screen == screenPersonas || a.screen == screenWorkers) && (msg.ev.Kind == talkhotkey.KindDown || msg.ev.Kind == talkhotkey.KindUp) {
		return a, a.rearmTalk(msg)
	}
	// The key is global by definition: deliver it to the conversation
	// whichever screen is showing, then keep pumping.
	var cmd tea.Cmd
	a.conversation, cmd = a.conversation.Update(msg)
	return a, tea.Batch(cmd, a.rearmTalk(msg))
}

// onWorkProjectionPoll takes tea.Msg because its case matches two types.
func (a App) onWorkProjectionPoll(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	a.conversation, cmd = a.conversation.Update(msg)
	return a, cmd
}

func (a App) onTalkHotkeyExited(msg talkHotkeyExitedMsg) (tea.Model, tea.Cmd) {
	if msg.lifetime != nil && msg.lifetime != a.runCtx {
		return a, nil
	}
	a.hotkey = nil
	a.conversation.talkExited()
	return a, nil
}

func (a App) onTalkControlClosed(msg talkControlClosedMsg) (tea.Model, tea.Cmd) {
	if msg.lifetime != nil && msg.lifetime != a.runCtx {
		return a, nil
	}
	// Nothing was registered, so the conversation loses no state: the
	// endpoint simply stops answering.
	a.control = nil
	return a, nil
}
