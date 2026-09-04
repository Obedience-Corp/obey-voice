package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
)

type backgroundRunner interface {
	BackgroundReady() <-chan struct{}
	BackgroundPending() bool
	RunBackgroundTurn(context.Context) error
}
type backgroundReadyMsg struct{}

func (m *conversationModel) waitBackground() tea.Cmd {
	runner, ok := m.deps.runner.(backgroundRunner)
	if !ok || runner.BackgroundReady() == nil {
		return nil
	}
	ready, ctx := runner.BackgroundReady(), m.deps.ctx
	return func() tea.Msg {
		select {
		case <-ready:
			return backgroundReadyMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *conversationModel) dispatchBackground() tea.Cmd {
	runner, ok := m.deps.runner.(backgroundRunner)
	if !ok || !runner.BackgroundPending() {
		return nil
	}
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning
	wg := m.deps.wg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		defer cancel()
		if ctx.Err() != nil {
			return textTurnDoneMsg{err: ctx.Err()}
		}
		return textTurnDoneMsg{err: runner.RunBackgroundTurn(ctx)}
	}
}
