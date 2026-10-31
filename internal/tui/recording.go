package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type recordingDoneMsg struct {
	lifetime context.Context
	err      error
	action   string
}

// Audio review owns the same foreground slot as text turns. The user pauses
// microphone input first; rejecting a busy request avoids overlapping capture,
// foreground speech, persona publication or Clear with original replay/retry.
func (m *conversationModel) requestRecording(action, id string, epoch uint64) tea.Cmd {
	if m.turnState != turnIdle || m.selectionPending() {
		m.commandError("Pause microphone input and wait for the current turn before reviewing a recording")
		return nil
	}
	operation := m.deps.replayRecording
	if action == "transcribe" {
		operation = m.deps.transcribeRecording
	}
	if operation == nil {
		m.commandError("Recording review is unavailable in this host")
		return nil
	}
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning
	m.reviewingRecording = true
	m.setStatus("Reviewing original recording", false)
	if action == "transcribe" {
		m.setStatus("Transcribing recording", false)
	}
	lifetime := m.deps.ctx
	return trackedCommand(lifetime, m.deps.wg, func() tea.Msg {
		defer cancel()
		err := ctx.Err()
		if err == nil {
			err = operation(ctx, id, epoch)
		}
		return recordingDoneMsg{lifetime: lifetime, err: err, action: action}
	})
}
func (m *conversationModel) finishRecording(msg recordingDoneMsg) tea.Cmd {
	if msg.lifetime != m.deps.ctx {
		return nil
	}
	m.turnCancel = nil
	m.turnState = turnIdle
	m.reviewingRecording = false
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.setStatus("Recording review stopped", false)
	case msg.err != nil:
		m.commandError(msg.err.Error())
	case msg.action == "transcribe":
		m.setStatus("Recording transcript updated", false)
	default:
		m.setStatus("Original recording finished", false)
	}
	return m.resumeListening()
}

func (m *conversationModel) recordingCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		var rows []string
		for _, turn := range m.plainTurns {
			if turn.input == nil || turn.input.Audio == nil {
				continue
			}
			state := "original unavailable"
			if m.deps.recordingAvailable != nil && m.deps.recordingAvailable(turn.input.ID, turn.input.CaptureEpoch) {
				state = "original available"
			}
			rows = append(rows, fmt.Sprintf("Recording %d · %.2f s · %s · transcript %s", len(rows)+1, float64(turn.input.Audio.SampleCount)/16000, state, turn.input.Transcript.State))
		}
		if len(rows) == 0 {
			rows = append(rows, "No recorded inputs in this conversation")
		}
		m.setStatus(strings.Join(rows, "; "), false)
		m.emit(events.Info{Message: strings.Join(rows, "\n") + "\nUse /input replay N or /input transcribe N."})
		return nil
	}

	if len(args) != 2 || (args[0] != "replay" && args[0] != "transcribe") {
		m.commandError("Use /input replay N or /input transcribe N (recording number from one)")
		return nil
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 1 {
		m.commandError("Choose a recording number from one")
		return nil
	}
	count := 0
	for _, turn := range m.plainTurns {
		if turn.input == nil || turn.input.Audio == nil {
			continue
		}
		count++
		if count == n {
			return m.requestRecording(args[0], turn.input.ID, turn.input.CaptureEpoch)
		}
	}
	m.commandError(fmt.Sprintf("Recording %d is unavailable", n))
	return nil
}
