package pipeline

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// RunTranscription recognizes one utterance without committing it to history,
// invoking the brain, routing tools, or playing a response. Review clients send
// voice_end to finalize offline STT and submit text only after user confirmation.
// Like RunTurn, it must be serialized against other pipeline operations.
func (p *Pipeline) RunTranscription(ctx context.Context) (string, error) {
	p.incActiveTurn()
	defer p.decActiveTurn()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	turn.to(TurnListening)
	// The requesting connection receives transcription_result as its terminal
	// event; this is not a conversational turn and emits no TurnMetrics.
	text, err := p.transcribeTurn(stt.WithLivePreview(ctx), metrics, turn)
	if p.Utterance != nil {
		p.Utterance.Clear()
	}
	return text, err
}
