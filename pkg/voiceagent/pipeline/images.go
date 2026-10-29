package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// ValidateImages is a read-only admission check; rejected input keeps its draft.
func (p *Pipeline) ValidateImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	return p.ImageValidator()(ctx, refs)
}

// Bind a provider at the serialized UI boundary before starting a read-only probe.
func (p *Pipeline) ImageValidator() func(context.Context, []chatmedia.Ref) error {
	validator, ok := p.Brain.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	})
	group := p.GroupParticipationEnabled
	return func(ctx context.Context, refs []chatmedia.Ref) error {
		if len(refs) == 0 {
			return nil
		}
		if group {
			return errors.New("image messages currently require a single-persona conversation; turn off group participation before sending")
		}
		if !ok {
			return errors.New("image input is supported with vision-capable Ollama models; switch models before sending")
		}
		return validator.ValidateImages(ctx, refs)
	}
}
