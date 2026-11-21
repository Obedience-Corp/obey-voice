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
//
// A picture belongs to the conversation, not to the persona that is selected
// when it is sent. A provider that keeps a picture on the record admits it
// whether or not its own model can view it. A provider that would drop the
// picture refuses it.
func (p *Pipeline) ImageValidator() func(context.Context, []chatmedia.Ref) error {
	keeper, keeps := p.Brain.(interface {
		ValidateNewImages(context.Context, []chatmedia.Ref) error
	})
	validator, ok := p.Brain.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	})
	return func(ctx context.Context, refs []chatmedia.Ref) error {
		if len(refs) == 0 {
			return nil
		}
		if keeps {
			return keeper.ValidateNewImages(ctx, refs)
		}
		if !ok {
			return errors.New("this provider does not support image input; choose a vision-capable Ollama model, Codex or Claude")
		}
		return validator.ValidateImages(ctx, refs)
	}
}

// refusedImages is a turn refused over its pictures after its draft was sent.
// Nothing went to a provider and nothing was recorded.
type refusedImages struct{ err error }

func (e refusedImages) Error() string { return e.err.Error() }
func (e refusedImages) Unwrap() error { return e.err }

// refuseImages marks a turn that failed its picture check as refused. A check
// that was itself canceled gave no answer: that turn was interrupted.
func refuseImages(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return refusedImages{err}
}

// ImagesRefused reports whether a turn failed only because its pictures were
// refused before any request, so a host can give the user their draft back.
func ImagesRefused(err error) bool {
	var refused refusedImages
	return errors.As(err, &refused)
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

// retainedTurns is the history the live provider builds its next request from.
func (p *Pipeline) retainedTurns() []brain.Turn {
	if working, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		return working.ContextHistory()
	}
	return p.Brain.History()
}

// viewsImages reports whether the live provider takes image input at all.
func (p *Pipeline) viewsImages() bool {
	_, ok := p.Brain.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	})
	return ok
}

// Validate retained user media after persona admission, on the serialized turn
// goroutine. This also protects continuations and resumed group discussions.
// A provider with no image input is sent no picture, so it has none to check.
func (p *Pipeline) validateRetainedImages(ctx context.Context) error {
	if brain.ReplaysImagesBestEffort(p.Brain) || !p.viewsImages() {
		return nil
	}
	for _, turn := range p.retainedTurns() {
		if turn.Role == "user" && len(turn.Attachments) > 0 {
			return p.validateAdmittedImages(ctx, turn.Attachments)
		}
	}
	return nil
}

// noteUnviewedImages tells the user which pictures of the conversation a
// provider with no image input answers without. It runs just before the
// request, after every exit that sends the provider nothing.
func (p *Pipeline) noteUnviewedImages(metrics *turnMetrics) {
	if p.viewsImages() {
		return
	}
	var unseen []chatmedia.Ref
	for _, turn := range p.retainedTurns() {
		if turn.Role == "user" {
			unseen = append(unseen, turn.Attachments...)
		}
	}
	if len(unseen) > 0 {
		p.imagesWithheldHook(metrics)("", unseen)
	}
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
