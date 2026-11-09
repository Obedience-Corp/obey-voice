package pipeline

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

type inputRetryKey struct{}

// RetryLastTurn resumes an unanswered, effect-free Ollama input under its
// original binding. Like other turn entry points it requires serialized use.
func (p *Pipeline) RetryLastTurn(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provider, ok := p.Brain.(brain.InputRetryProvider)
	if !ok {
		return fmt.Errorf("this provider does not support /retry")
	}
	retry, ok := provider.FailedInputRetry()
	if !ok {
		return fmt.Errorf("no safe failed input to retry; send a new request")
	}
	if retry.BindingRevision != p.inputBindingRevision.Load() {
		provider.DiscardInputRetry()
		return fmt.Errorf("voice binding changed; send a new request")
	}
	if p.Native != nil {
		return fmt.Errorf("audio inputs require a new recording")
	}
	retry.Envelope.ResponseID = newWorkID()
	return p.runTextTurn(context.WithValue(ctx, inputRetryKey{}, retry), retry.Input, false)
}

func inputRetryFromContext(ctx context.Context) (brain.InputRetry, bool) {
	r, ok := ctx.Value(inputRetryKey{}).(brain.InputRetry)
	return r, ok
}

func (p *Pipeline) discardInputRetry() {
	if provider, ok := p.Brain.(brain.InputRetryProvider); ok {
		provider.DiscardInputRetry()
	}
}
