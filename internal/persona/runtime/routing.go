package personaruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
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
	if !background {
		s.EndDiscussion("superseded")
	}
	if s.fixedResponder() {
		if handover, ok, err := s.routeExplicitHandover(ctx, input, background); ok {
			return handover, err
		}
		envelope, err := s.Admit(ctx)
		if err != nil {
			return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
		}
		admitted.Envelope = envelope
		return admitted, nil
	}
	roster, participants, err := s.conversationRoster()
	if err != nil {
		return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
	}
	admitted.AgentMentionControl = agentMentionControl(roster)
	bare := false
	if !background {
		exact := roster.ResolveForegroundControl(input)
		switch exact.Kind {
		case summon.ExactControlValid:
			if err := s.refuseOneWaySwitch(exact.Address); err != nil {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
			}
			return s.routeExactControl(ctx, input, exact)
		case summon.ExactControlInvalid:
			if exact.Err != nil {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", exact.Err)
			}
			return admitted, wrapIdentityRefusalFailure(ctx, "route", errors.New("invalid exact identity control"))
		}
		if s.groupParticipation() {
			groupAdmission, err := s.routeAudience(ctx, input)
			groupAdmission.AgentMentionControl = admitted.AgentMentionControl
			return groupAdmission, err
		}
		address, err := s.resolveAddress(ctx, input, "user", s.humanSpeakerName(""), roster, participants)
		if err != nil {
			if isIdentityRefusal(err) {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
			}
			return admitted, classifyAdmissionFailure(ctx, "route", err)
		}
		if address.PersonaID != "" {
			if err := s.refuseOneWaySwitch(address); err != nil {
				return admitted, wrapIdentityRefusalFailure(ctx, "route", err)
			}
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

// fixedResponder reports whether this conversation stays with its live
// persona: the live provider keeps no shared history, and no other persona is
// on a provider that does. Routing then leaves every input to the live persona
// with no roster, as before persona selection existed for that provider. A
// conversation on Claude with only other personas on Claude stays this way too;
// there an explicit switch or @mention still hands it over.
func (s *Selector) fixedResponder() bool {
	if brain.KeepsSharedHistory(s.opts.Pipeline.Brain) {
		return false
	}
	profiles, _, err := s.opts.Catalog.ListWithIssues()
	if err != nil {
		return true
	}
	live := s.Snapshot().PersonaID
	for _, p := range profiles {
		if p.Inactive || p.ID == live {
			continue
		}
		if provider, _ := s.canTakeOver(p); slices.Contains(sharedHistoryProviders, provider) {
			return false
		}
	}
	return true
}

// canTakeOver resolves the provider a persona would answer on and reports
// whether the conversation can be handed to it and back.
func (s *Selector) canTakeOver(p *persona.Profile) (string, bool) {
	cfg, _, err := persona.ResolveAudioConfig(s.opts.Base, p)
	if err != nil {
		return "", false
	}
	return cfg.BrainProvider, slices.Contains(sharedHistoryProviders, cfg.BrainProvider) || slices.Contains(sessionCatchUpProviders, cfg.BrainProvider)
}

// routeExplicitHandover honours an explicit switch or @mention in a
// conversation that otherwise stays with a live persona on Claude.
func (s *Selector) routeExplicitHandover(ctx context.Context, input string, background bool) (pipeline.ResponseAdmission, bool, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	if background || !brain.KeepsPersonaSession(s.opts.Pipeline.Brain) {
		return admitted, false, nil
	}
	roster, _, err := s.conversationRoster()
	if err != nil {
		return admitted, false, nil
	}
	exact := roster.ResolveForegroundControl(input)
	if exact.Kind != summon.ExactControlValid || !explicitHandover(exact.Address) {
		return admitted, false, nil
	}
	admitted, err = s.routeExactControl(ctx, input, exact)
	if err == nil && s.fixedResponder() {
		// The conversation stays without a roster, as Admit leaves it.
		s.setGroupParticipants(nil)
	}
	admitted.AgentMentionControl = agentMentionControl(roster)
	return admitted, true, err
}

func explicitHandover(address summon.Address) bool {
	return address.Reason == summon.ReasonExplicitSwitch || address.Reason == "explicit-mention"
}

// refuseOneWaySwitch keeps the conversation on a live provider that could not
// take it back, unless the user asked for the move with an explicit switch or
// @mention. A persona on Claude can take it back, so it is addressed by name
// like any other.
func (s *Selector) refuseOneWaySwitch(address summon.Address) error {
	live := s.Snapshot()
	if brain.KeepsSharedHistory(s.opts.Pipeline.Brain) || brain.KeepsPersonaSession(s.opts.Pipeline.Brain) || address.PersonaID == "" || address.PersonaID == live.PersonaID {
		return nil
	}
	if explicitHandover(address) {
		return nil
	}
	target := address.PersonaID
	p, err := s.opts.Catalog.Load(target)
	if err != nil {
		return err
	}
	if provider, ok := s.canTakeOver(p); !ok {
		return fmt.Errorf("persona %q: provider %q has no verified shared history projection", p.ID, provider)
	}
	if p.DisplayName != "" {
		target = p.DisplayName
	}
	return fmt.Errorf("%s cannot rejoin this conversation once %s takes it over; say \"switch to %s\" to hand it over", live.DisplayName, target, target)
}
