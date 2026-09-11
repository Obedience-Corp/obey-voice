package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const backgroundRecoveryInterval = 500 * time.Millisecond

type backgroundRunner interface {
	BackgroundReady() <-chan struct{}
	BackgroundPending() bool
	RunBackgroundTurn(context.Context) error
}
type backgroundReadyMsg struct{}
type backgroundRecoveryMsg struct{}
type backgroundPauseDoneMsg struct {
	acknowledged bool
	err          error
	generation   uint64
}

type backgroundCapturePauser interface {
	PauseForCompletion(context.Context) (bool, error)
}

type backgroundCaptureReadiness interface {
	BackgroundCaptureReady() bool
}

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

// backgroundRecoveryCmd is deliberately level-triggered. BackgroundReady is
// an optimization for prompt wakeups, but a notification can be published
// before the update loop starts waiting on it. Rechecking the inbox keeps a
// pending result from depending on that edge.
func (m *conversationModel) backgroundRecoveryCmd() tea.Cmd {
	ctx := m.deps.ctx
	if ctx == nil {
		return nil
	}
	return func() tea.Msg {
		timer := time.NewTimer(backgroundRecoveryInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
			return backgroundRecoveryMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}

// reconcileBackground observes the host's current level and then applies the
// ordinary serialized turn gate. It never claims work while captured input or
// another turn owns the pipeline.
func (m *conversationModel) reconcileBackground() tea.Cmd {
	return m.resumeListening()
}

func (m *conversationModel) hasCapturedInput() bool {
	capture, ok := m.deps.runner.(interface{ HasCapturedInput() bool })
	return ok && capture.HasCapturedInput()
}

func (m *conversationModel) requestBackgroundPause() tea.Cmd {
	pauser, ok := m.deps.runner.(backgroundCapturePauser)
	if !ok || m.pendingPause || m.backgroundPauseAcknowledged {
		return nil
	}
	if m.hasCapturedInput() {
		return nil
	}
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.pendingPause = true
	generation := m.backgroundPauseGeneration
	return func() tea.Msg {
		defer cancel()
		acknowledged, err := pauser.PauseForCompletion(ctx)
		return backgroundPauseDoneMsg{acknowledged: acknowledged, err: err, generation: generation}
	}
}
func (m *conversationModel) dispatchBackground() tea.Cmd {
	if capture, ok := m.deps.runner.(interface{ HasCapturedInput() bool }); ok && capture.HasCapturedInput() {
		return nil
	}
	if capture, ok := m.deps.runner.(backgroundCaptureReadiness); ok && !capture.BackgroundCaptureReady() {
		return nil
	}
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
