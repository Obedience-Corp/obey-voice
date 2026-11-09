package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *conversationModel) requestInputRetry() tea.Cmd {
	if m.selectionPending() || m.reviewingRecording {
		m.commandError("finish the current selection or recording review before /retry")
		return nil
	}
	if _, ok := m.deps.runner.(interface{ RetryLastTurn(context.Context) error }); !ok {
		m.commandError("/retry is unavailable")
		return nil
	}
	switch m.turnState {
	case turnIdle:
		return m.dispatchInputRetry()
	case turnVoiceListening:
		if m.canCancelVoice != nil && !m.canCancelVoice.Load() {
			m.commandError("busy — try /retry after this turn")
			return nil
		}
		m.pendingInputRetry = true
		m.turnState = turnVoiceCanceling
		if m.canCancelVoice != nil {
			m.canCancelVoice.Store(false)
		}
		if m.turnCancel != nil {
			m.turnCancel()
		}
		return nil
	default:
		m.commandError("busy — try /retry after this turn")
		return nil
	}
}

func (m *conversationModel) dispatchInputRetry() tea.Cmd {
	runner := m.deps.runner.(interface{ RetryLastTurn(context.Context) error })
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel, m.turnState = cancel, turnTextRunning
	epoch, lifetime := m.noticeEpoch, m.deps.ctx
	return trackedCommand(lifetime, m.deps.wg, func() tea.Msg {
		defer cancel()
		return textTurnDoneMsg{lifetime: lifetime, err: runner.RetryLastTurn(ctx), noticeEpoch: epoch}
	})
}
