package personaruntime

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

var ErrNativeUnqualified = errors.New("native activation requires verified context accounting and task qualification")

// NativeSelectionHooks joins host-owned media/capture to the existing selector.
// Prepare must use the supplied publisher and selection session/epoch, never a
// global media store. Authorize is mandatory: protocol readiness alone cannot
// qualify context safety or quality. Nil hooks preserve guarded Standard paths.
type NativeSelectionHooks struct {
	TextOnly     bool
	Prepare      func(context.Context, *config.Config, persona.Audio, events.PersonaSelection, func(context.Context, func() error) error) (*voiceagent.NativeCandidate, error)
	Authorize    func(context.Context, *persona.SessionBinding, *voiceagent.NativeCandidate) error
	PrepareInput func(context.Context, *config.Config, *voiceagent.NativeCandidate) (pipeline.InputBinding, func(), error)
}

// captionPublisher holds a read lease only while materializing a finished result.
// The selection writer acquires its lease after cancel/join, before persistence.
// It never joins producers while holding the publication writer lock.
func (s *Selector) captionPublisher(token string, expected events.PersonaSelection) func(context.Context, func() error) error {
	return func(ctx context.Context, publish func() error) error {
		s.publicationMu.RLock()
		defer s.publicationMu.RUnlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		scope := s.opts.Pipeline.ConversationScope()
		if !scope.Valid() || s.publicationClosed || s.bindingToken != token || s.Snapshot().SessionID != expected.SessionID || s.opts.Pipeline.ConversationRetired() || scope.ConversationEpoch != expected.ConversationEpoch {
			return errors.New("caption belongs to a retired persona binding")
		}
		if publish == nil {
			return errors.New("caption materialization is missing")
		}
		return publish()
	}
}
