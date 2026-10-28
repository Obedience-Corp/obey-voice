package personaruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// Route is the shared finalized-input operation for text and spoken requests.
// Partial transcripts never call this hook. A missing address keeps the
// selected responder; the audio front end owns echo prevention independently.
func (s *Selector) Route(ctx context.Context, input string, background bool) (pipeline.ResponseAdmission, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	if err := ctx.Err(); err != nil {
		return admitted, classifyAdmissionFailure(ctx, "route", err)
	}
	bare := false
	if !background {
		roster, participants, err := s.conversationRoster()
		if err != nil {
			return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
		}
		exact := roster.ResolveExactControl(input)
		switch exact.Kind {
		case summon.ExactControlValid:
			return s.routeExactControl(ctx, input, exact)
		case summon.ExactControlInvalid:
			if exact.Err != nil {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", exact.Err)
			}
			return admitted, wrapIdentityRefusalFailure(ctx, "route", errors.New("invalid exact identity control"))
		}
		address, err := s.resolveAddress(ctx, input, "user", s.humanSpeakerName(""), roster, participants)
		if err != nil {
			if isIdentityRefusal(err) {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
			}
			return admitted, classifyAdmissionFailure(ctx, "route", err)
		}
		if address.PersonaID != "" {
			if _, err := s.Select(ctx, address.PersonaID, address.Reason); err != nil {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
			}
			admitted.Request, bare = address.Remainder, address.Bare
		}
	}
	envelope, err := s.Admit(ctx)
	if err != nil {
		return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
	}
	admitted.Envelope = envelope
	if bare {
		admitted.LocalReply = envelope.DisplayName + " here."
	}
	return admitted, nil
}

func (s *Selector) warnCatalogIssues(issues []persona.CatalogIssue) {
	if s.opts.Pipeline.Events == nil {
		return
	}
	for _, issue := range issues {
		s.opts.Pipeline.Events.Emit(events.Error{
			Stage:   "persona_roster",
			Message: fmt.Sprintf("persona %q is unavailable until repaired: %v", issue.ID, issue.Err),
		})
	}
}
