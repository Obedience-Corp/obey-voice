package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func (m *conversationModel) updateInputTranscript(update events.InputTranscriptUpdated) {
	if update.Transcript.Validate() != nil {
		return
	}
	changed := false
	for i := range m.items {
		item := &m.items[i]
		if item.input == nil || item.input.ID != update.UtteranceID || item.input.CaptureEpoch != update.CaptureEpoch {
			continue
		}
		item.input = item.input.Clone()
		item.input.Transcript = update.Transcript
		item.sys = renderSpeakerUserTurn(item.name, utterance.DisplayAudioInput(item.inputTyped, item.input))
		changed = true
	}
	for i := range m.plainTurns {
		turn := &m.plainTurns[i]
		if turn.input == nil || turn.input.ID != update.UtteranceID || turn.input.CaptureEpoch != update.CaptureEpoch {
			continue
		}
		turn.input = turn.input.Clone()
		turn.input.Transcript = update.Transcript
		turn.text = utterance.DisplayAudioInput(turn.inputTyped, turn.input)
	}
	if changed {
		m.refreshContent()
		m.applyFollow()
	}
}
