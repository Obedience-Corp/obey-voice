package pipeline

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
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
	return func(ctx context.Context, refs []chatmedia.Ref) error {
		if len(refs) == 0 {
			return nil
		}
		if !ok {
			return errors.New("this provider does not support image input; choose a vision-capable Ollama model or Codex")
		}
		return validator.ValidateImages(ctx, refs)
	}
}

// Validate retained user media after persona admission, on the serialized turn
// goroutine. This also protects continuations and resumed group discussions.
func (p *Pipeline) validateRetainedImages(ctx context.Context) error {
	turns := p.Brain.History()
	if working, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		turns = working.ContextHistory()
	}
	for _, turn := range turns {
		if turn.Role == "user" && len(turn.Attachments) > 0 {
			return p.ValidateImages(ctx, turn.Attachments)
		}
	}
	return nil
}
