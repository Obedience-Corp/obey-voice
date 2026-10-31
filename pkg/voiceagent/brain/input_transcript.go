package brain

import (
	"errors"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type InputTranscriptUpdater interface {
	UpdateInputTranscript(string, uint64, utterance.Transcript) error
}
type inputTranscriptKey struct {
	ID    string
	Epoch uint64
}

// UpdateInputTranscript is called only for an authorized, quiescent native
// binding. Caption values are immutable synchronized updates; readers obtain
// detached materialized canonical/context snapshots, never a mutable alias.
func (o *OllamaBrain) UpdateInputTranscript(id string, epoch uint64, transcript utterance.Transcript) error {
	if err := transcript.Validate(); err != nil {
		return err
	}
	if transcript.State != utterance.Ready && transcript.State != utterance.Unavailable {
		return errors.New("caption must be ready or unavailable")
	}
	if len(transcript.Text) > 256<<10 || len(transcript.Reason) > 2048 {
		return errors.New("caption exceeds metadata limit")
	}
	found := false
	for _, turn := range o.canonical {
		if turn.Role == "user" && turn.Utterance != nil && turn.Utterance.ID == id && turn.Utterance.CaptureEpoch == epoch {
			found = true
			break
		}
	}
	if !found {
		return errors.New("caption belongs to an unknown or retired utterance")
	}
	o.inputTranscripts.Store(inputTranscriptKey{ID: id, Epoch: epoch}, transcript)
	return nil
}
func (o *OllamaBrain) effectiveTurn(turn Turn) Turn {
	if turn.Utterance == nil {
		return turn
	}
	turn.Utterance = turn.Utterance.Clone()
	if update, ok := o.inputTranscripts.Load(inputTranscriptKey{ID: turn.Utterance.ID, Epoch: turn.Utterance.CaptureEpoch}); ok {
		transcript := update.(utterance.Transcript)
		turn.Utterance.Transcript = transcript
		if turn.TypedInput == "" && transcript.State == utterance.Ready {
			turn.Content = transcript.Text
		}
	}
	// Caption metadata cannot invent a typed instruction or overwrite one.
	if strings.TrimSpace(turn.TypedInput) != "" {
		turn.Content = turn.TypedInput
	}
	return turn
}
func (o *OllamaBrain) effectiveTurns(turns []Turn) []Turn {
	copied := deepCopyTurns(turns)
	for i := range copied {
		copied[i] = o.effectiveTurn(copied[i])
	}
	return copied
}
