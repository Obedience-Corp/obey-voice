package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type personaSelectionRequestMsg struct{ id string }

type personaSelectionResultMsg struct {
	id              string
	selection       events.PersonaSelection
	cfg             *config.Config
	err             error
	runtime         *ConversationRuntime
	sessionID       string
	runtimeEpoch    uint64
	requestSequence uint64
}

// configureSelection makes the conversation owner the lifetime of selection
// work. The Personas screen is recreated when it is reopened, but this state
// remains attached to the live conversation and its runtime.
func (m *conversationModel) configureSelection(rt *ConversationRuntime) {
	if m.selectionCancel != nil {
		m.selectionCancel()
	}
	m.selectionRuntime = rt
	m.selection = events.PersonaSelection{}
	m.selectionRevision = 0
	m.selectionConfig = nil
	if rt != nil {
		m.selectionConfig = rt.SelectionConfig
	}
	m.selectionEpoch++
	ctx := m.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.selectionCtx, m.selectionCancel = context.WithCancel(ctx)
	m.selectionSession = m.sessionID
	m.selectionBusy = false
	m.selectionQueue = nil
}

func (m *conversationModel) cancelSelection() {
	if m.selectionCancel != nil {
		m.selectionCancel()
		m.selectionCancel = nil
	}
	m.selectionRuntime = nil
	m.selectionBusy = false
	m.selectionQueue = nil
	m.selectionEpoch++
}

func (m *conversationModel) selectionPending() bool {
	return m.selectionBusy || len(m.selectionQueue) > 0
}

// enqueuePersonaSelection is the only admission point for in-chat choices.
// It leaves the request queued while a turn owns the pipeline and starts it
// only after that turn has returned to idle.
func (m *conversationModel) enqueuePersonaSelection(id string) tea.Cmd {
	if m.selectionRuntime == nil || m.selectionRuntime.SelectPersona == nil {
		return nil
	}
	m.selectionQueue = append(m.selectionQueue, id)
	if m.turnState != turnIdle || m.selectionBusy {
		return nil
	}
	return m.startNextPersonaSelection()
}

func (m *conversationModel) startNextPersonaSelection() tea.Cmd {
	if m.selectionRuntime == nil || m.selectionRuntime.SelectPersona == nil || m.selectionBusy || m.turnState != turnIdle || len(m.selectionQueue) == 0 {
		return nil
	}
	m.selectionBusy = true
	m.selectionRequest++
	runtime, callback := m.selectionRuntime, m.selectionRuntime.SelectPersona
	epoch, request, session := m.selectionEpoch, m.selectionRequest, m.selectionSession
	ctx := m.selectionCtx
	if ctx == nil {
		ctx = context.Background()
	}
	id := m.selectionQueue[0]
	wg := m.deps.wg
	return trackedCommand(m.deps.ctx, wg, func() tea.Msg {
		selection, cfg, err := callback(ctx, id)
		return personaSelectionResultMsg{id: id, selection: selection, cfg: cfg, err: err,
			runtime: runtime, sessionID: session, runtimeEpoch: epoch, requestSequence: request}
	})
}

// finishPersonaSelection accepts only the result for the current runtime,
// session, epoch, request, and FIFO head. It returns whether App may publish
// the result and the next selection command, if any.
func (m *conversationModel) finishPersonaSelection(msg personaSelectionResultMsg) (bool, tea.Cmd) {
	if !m.selectionBusy || m.selectionRuntime != msg.runtime || m.selectionSession != msg.sessionID || m.selectionEpoch != msg.runtimeEpoch || m.selectionRequest != msg.requestSequence || len(m.selectionQueue) == 0 || m.selectionQueue[0] != msg.id {
		return false, nil
	}
	m.selectionQueue = m.selectionQueue[1:]
	m.selectionBusy = false
	return true, m.startNextPersonaSelection()
}

// applySelectionSnapshot is called only by the Bubble Tea update goroutine.
// It advances display state monotonically so a late selection event cannot
// relabel the current session with an older binding.
func (m *conversationModel) applySelectionSnapshot(selection events.PersonaSelection) {
	if m.sessionID != "" && selection.SessionID != "" && selection.SessionID != m.sessionID {
		return
	}
	if selection.Revision < m.selectionRevision {
		return
	}
	if selection.Revision == m.selectionRevision && m.selection.PersonaID != "" && selection.BindingHash != "" && selection.BindingHash != m.selection.BindingHash {
		return
	}
	m.selection = selection
	m.selectionRevision = selection.Revision
	if selection.DisplayName != "" {
		m.agentName = selection.DisplayName
	}
}
