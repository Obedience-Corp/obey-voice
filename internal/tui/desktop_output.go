package tui

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	tea "github.com/charmbracelet/bubbletea"
)

type desktopOutputResult struct {
	message string
	err     error
}

// Native device teardown and recorder flushing run off the UI loop, and join
// the conversation's existing shutdown drain before its player is closed.
func (m *desktopModel) outputAction(kind string) tea.Cmd {
	if m.outputActionBusy {
		return nil
	}
	if m.rt == nil || m.rt.Pipeline == nil || m.rt.Pipeline.Player == nil {
		m.outputMessage, m.outputError = "Audio output is unavailable in this conversation", true
		return nil
	}
	p := m.rt.Pipeline
	ctx := m.app.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if kind == "audio_reset" {
		if _, ok := p.Player.(audio.OutputResetter); !ok {
			m.outputMessage, m.outputError = "This audio output does not support reset", true
			return nil
		}
		if m.app.conversation.turnCancel != nil {
			m.app.conversation.turnCancel()
		}
	}
	m.outputActionBusy, m.outputError = true, false
	m.outputMessage = "Updating audio output…"
	return trackedCommand(ctx, m.app.wg, func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return desktopOutputResult{err: err}
		}
		if kind == "audio_reset" {
			err := p.ResetAudioOutput(ctx)
			return desktopOutputResult{message: "Audio output reset. It will reopen with the next spoken reply.", err: err}
		}
		capture, ok := p.Player.(audio.DiagnosticCapturer)
		if !ok {
			return desktopOutputResult{err: errors.New("This audio output does not support diagnostic capture")}
		}
		if kind == "audio_capture_start" {
			_, err := capture.StartDiagnosticCapture(ctx)
			return desktopOutputResult{message: "Diagnostic audio capture enabled for up to 60 seconds", err: err}
		}
		capture.StopDiagnosticCapture()
		return desktopOutputResult{message: "Diagnostic audio capture stopped"}
	})
}

func (m desktopModel) outputState(state *DesktopState) {
	state.OutputActionBusy, state.OutputMessage, state.OutputError = m.outputActionBusy, m.outputMessage, m.outputError
	if m.rt == nil || m.rt.Pipeline == nil {
		return
	}
	player := m.rt.Pipeline.Player
	_, state.OutputResetAvailable = player.(audio.OutputResetter)
	if provider, ok := player.(audio.OutputStatusProvider); ok {
		status := provider.OutputStatus()
		state.OutputStatus = &status
	}
	if capture, ok := player.(audio.DiagnosticCapturer); ok {
		status := capture.DiagnosticCaptureStatus()
		state.OutputCapture = &status
	}
}
