package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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
			return errors.New("this provider does not support image input; choose a vision-capable Ollama model, Codex or Claude")
		}
		return validator.ValidateImages(ctx, refs)
	}
}

// validateAdmittedImages checks the pictures of a turn that is already part of
// the conversation. A provider that keeps a picture on the record for a model
// that cannot view it answers the turn; any other is asked as for new input.
func (p *Pipeline) validateAdmittedImages(ctx context.Context, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	if admitted, ok := p.Brain.(interface {
		ValidateAdmittedImages(context.Context, []chatmedia.Ref) error
	}); ok {
		return admitted.ValidateAdmittedImages(ctx, refs)
	}
	return p.ValidateImages(ctx, refs)
}

// Validate retained user media after persona admission, on the serialized turn
// goroutine. This also protects continuations and resumed group discussions.
// A persona that cannot view a picture someone else was shown still answers.
func (p *Pipeline) validateRetainedImages(ctx context.Context, metrics *turnMetrics) error {
	if brain.ReplaysImagesBestEffort(p.Brain) {
		return nil
	}
	turns := p.Brain.History()
	if working, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		turns = working.ContextHistory()
	}
	_, views := p.Brain.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	})
	var unseen []chatmedia.Ref
	for _, turn := range turns {
		if turn.Role != "user" || len(turn.Attachments) == 0 {
			continue
		}
		if views {
			return p.validateAdmittedImages(ctx, turn.Attachments)
		}
		unseen = append(unseen, turn.Attachments...)
	}
	if len(unseen) > 0 {
		p.imagesWithheldHook(metrics)("", unseen)
	}
	return nil
}

// imagesWithheldHook tells the user, once for each picture and persona, that
// a reply is written without it. model is empty for a provider that takes no
// image input at all.
func (p *Pipeline) imagesWithheldHook(metrics *turnMetrics) func(model string, refs []chatmedia.Ref) {
	persona, name := "", "The model"
	if metrics != nil {
		persona = metrics.response.PersonaID
		if display := strings.TrimSpace(metrics.response.DisplayName); display != "" {
			name = display
		}
	}
	return func(model string, refs []chatmedia.Ref) {
		epoch := p.ConversationScope().ConversationEpoch
		var names []string
		p.withheldMu.Lock()
		if p.withheldImages == nil {
			p.withheldImages = map[string]bool{}
		}
		for _, ref := range refs {
			key := strings.Join([]string{epoch, persona, model, ref.ID}, "\x00")
			if !p.withheldImages[key] {
				p.withheldImages[key] = true
				names = append(names, ref.Name)
			}
		}
		p.withheldMu.Unlock()
		if len(names) == 0 {
			return
		}
		reason := "this provider cannot view images"
		if model != "" {
			reason = model + " cannot view images"
		}
		p.emit(events.Info{Message: fmt.Sprintf("%s was not shown %s, because %s.", name, strings.Join(names, ", "), reason)})
	}
}
