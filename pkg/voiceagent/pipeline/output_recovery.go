package pipeline

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// ResetAudioOutput revokes completion delivery and resets only the output
// lifecycle. History, providers and conversation identity remain owned by the
// existing pipeline. The host cancels its active foreground turn before reset.
func (p *Pipeline) ResetAudioOutput(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resetter, ok := p.Player.(audio.OutputResetter)
	if !ok {
		return errors.New("audio output reset is unavailable")
	}
	if c := p.WorkCoordinator(); c != nil {
		if err := c.RevokeBinding(c.Scope()); err != nil {
			return err
		}
	}
	p.stopBackchannel()
	return resetter.ResetOutput(ctx)
}
