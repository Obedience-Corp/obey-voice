package personaruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func (s *Selector) conversationRoster() (*summon.Roster, []brain.GroupParticipant, error) {
	profiles, issues, err := s.opts.Catalog.ListWithIssues()
	if err != nil {
		return nil, nil, err
	}
	s.warnCatalogIssues(issues)
	identities := make([]summon.Identity, 0, len(profiles)+len(issues))
	participants := make([]brain.GroupParticipant, 0, len(profiles))
	for _, p := range profiles {
		if p.Inactive {
			continue
		}
		aliases := slices.Clone(p.Aliases)
		if p.ID == s.opts.Initial.PersonaID {
			aliases = append(aliases, s.opts.Base.NameSummonAliases...)
		}
		identities = append(identities, summon.Identity{ID: p.ID, DisplayName: p.DisplayName, Aliases: aliases})
		if words := strings.Fields(p.DisplayName); len(words) > 1 {
			aliases = append(slices.Clone(aliases), words[0])
		}
		participants = append(participants, brain.GroupParticipant{Name: p.DisplayName, Aliases: aliases})
	}
	// Broken profile IDs remain host-side lookup entries. Never send storage IDs
	// as group members; an explicit broken target still fails selection.
	for _, issue := range issues {
		identities = append(identities, summon.Identity{ID: issue.ID, DisplayName: issue.ID})
	}
	roster, err := summon.NewRoster(identities)
	return roster, participants, err
}

// agentMentionName gives speech the display name of the persona an @handle
// addresses, by the same immutable ID/name/alias resolution handoffs use. An
// unrecognized handle is not an agent control and is spoken as written.
func agentMentionName(roster *summon.Roster) func(string) (string, bool) {
	return func(target string) (string, bool) {
		exact := roster.ResolveAgentMention("@" + target)
		if exact.Kind != summon.ExactControlValid {
			return "", false
		}
		return roster.DisplayName(exact.Address.PersonaID), true
	}
}

func (s *Selector) resolveAddress(ctx context.Context, text, account, speaker string, roster *summon.Roster, participants []brain.GroupParticipant) (summon.Address, error) {
	if err := ctx.Err(); err != nil {
		return summon.Address{}, classifyAdmissionFailure(ctx, "resolve_addressee", err)
	}
	if account == "agent" {
		exact := roster.ResolveAgentMention(text)
		switch exact.Kind {
		case summon.ExactControlValid:
			return exact.Address, nil
		case summon.ExactControlInvalid:
			return summon.Address{}, wrapIdentityRefusalFailure(ctx, "resolve_addressee", exact.Err)
		}
	}
	acousticName := ""
	if account == "user" {
		acousticName = admission.AcousticName(ctx)
	}
	// No named agent means no selection to classify. This is a candidate
	// check, not an intent grammar: named mentions still need semantic routing.
	if !roster.Mentions(text) && acousticName == "" {
		return summon.Address{Remainder: text}, nil
	}
	resolver := s.opts.AddressResolver
	if resolver == nil {
		resolver, _ = s.opts.Pipeline.Brain.(brain.AddresseeResolver)
	}
	viaDecision := s.opts.Pipeline.Decisions.Enabled(decision.AddresseeResolution)
	if resolver == nil && !viaDecision {
		// Compatibility for hosts/providers without a semantic classifier. Agent
		// handoffs require semantic support; legacy grammar cannot infer intent.
		if account == "agent" {
			return summon.Address{Remainder: text}, nil
		}
		address, err := roster.Resolve(text)
		if err != nil {
			return summon.Address{}, wrapIdentityRefusalFailure(ctx, "resolve_addressee", err)
		}
		return address, nil
	}
	request := brain.AddressRequest{Participants: participants, Current: s.Snapshot().DisplayName, Speaker: speaker, Account: account, Text: text}
	if account == "user" {
		request.AcousticName = acousticName
	}
	turns := s.Transcript()
	// Latest assistant is already canonical on a handoff; keep it only in Text.
	if account == "agent" && len(turns) > 0 && turns[len(turns)-1].Role == "assistant" {
		turns = turns[:len(turns)-1]
	}
	for i := len(turns) - 1; i >= 0 && len(request.Conversation) < 8; i-- {
		t := turns[i]
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		name, kind := "Unknown", "user"
		if t.Role == "user" {
			name = s.humanSpeakerName(t.Speaker)
		}
		if t.Role == "assistant" {
			name, kind = strings.TrimSpace(t.PersonaDisplayName), "agent"
			if name == "" {
				name = "Unknown"
			}
		}
		content := []rune(brain.CleanHistoricalAssistantText(t.Content))
		if len(content) > 1000 {
			content = content[:1000]
		}
		request.Conversation = append(request.Conversation, brain.AddressMessage{Name: name, Account: kind, Text: string(content)})
	}
	slices.Reverse(request.Conversation)
	started := time.Now()
	var trace addresseeTrace
	decided, err := s.classifyAddressee(ctx, resolver, viaDecision, request, &trace)
	if err != nil {
		trace.resolver = resolutionFailed
		s.emitAddresseeTrace(trace, started)
		return summon.Address{}, classifyAdmissionFailure(ctx, "resolve_addressee", err)
	}
	if decided == nil {
		trace.resolver = resolvedByCurrent
		if account == "agent" {
			s.emitAddresseeTrace(trace, started)
			return summon.Address{Remainder: text}, nil
		}
		address, err := roster.Resolve(text)
		if err != nil || address.PersonaID != "" {
			trace.resolver = resolvedByGrammar
		}
		s.emitAddresseeTrace(trace, started)
		if err != nil {
			return summon.Address{}, wrapIdentityRefusalFailure(ctx, "resolve_addressee", err)
		}
		return address, nil
	}
	s.emitAddresseeTrace(trace, started)
	chosen := *decided
	if err := ctx.Err(); err != nil {
		return summon.Address{}, classifyAdmissionFailure(ctx, "resolve_addressee", err)
	}
	switch chosen.Kind {
	case "none":
		return summon.Address{Remainder: text}, nil
	case "ambiguous":
		// Lookup preserves the existing precise collision error when possible.
		if _, err := roster.Lookup(chosen.Name); err != nil {
			if _, ok := err.(*summon.AmbiguousTargetError); ok {
				return summon.Address{}, wrapIdentityRefusalFailure(ctx, "resolve_addressee", err)
			}
		}
		return summon.Address{}, wrapIdentityRefusalFailure(
			ctx,
			"resolve_addressee",
			fmt.Errorf("conversation addressee is ambiguous; address one persona by a unique name"),
		)
	case "address":
		address, err := roster.Lookup(chosen.Name)
		if err != nil {
			var unknown *summon.UnknownTargetError
			if errors.As(err, &unknown) {
				// Semantic routing can choose only from the agent roster.
				// An unlisted model/person/topic is conversation content, not
				// a failed identity control. Exact controls were checked by Route.
				return summon.Address{Remainder: text}, nil
			}
			return summon.Address{}, wrapIdentityRefusalFailure(ctx, "resolve_addressee", err)
		}
		// Keep the user's words exactly, including the name at any position.
		address.Remainder, address.Bare = text, chosen.Bare && account == "user"
		return address, nil
	default:
		return summon.Address{}, wrapInvalidClassifierOutput(
			"resolve_addressee",
			fmt.Errorf("invalid conversation addressee decision %q", chosen.Kind),
		)
	}
}

func (s *Selector) routeExactControl(ctx context.Context, input string, exact summon.ExactControl) (pipeline.ResponseAdmission, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	var participants []brain.GroupParticipant
	greeting := false
	selected, err := s.selectAtBoundaryWithCommitCheck(ctx, exact.Address.PersonaID, exact.Address.Reason, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		roster, refreshedParticipants, err := s.conversationRoster()
		if err != nil {
			return err
		}
		revalidated := roster.ResolveForegroundControl(input)
		switch revalidated.Kind {
		case summon.ExactControlValid:
			if revalidated.Address.PersonaID != exact.Address.PersonaID {
				return fmt.Errorf("exact identity control changed target while preparing")
			}
			greeting = addressedGreeting(input, roster, revalidated)
		case summon.ExactControlInvalid:
			if revalidated.Err != nil {
				return revalidated.Err
			}
			return fmt.Errorf("exact identity control became invalid")
		default:
			return fmt.Errorf("exact identity control no longer matches current roster")
		}
		participants = refreshedParticipants
		admitted.AgentMentionName = agentMentionName(roster)
		return nil
	})
	if err != nil {
		return admitted, wrapIdentityRefusalFailure(ctx, "route_exact_control", err)
	}
	s.setGroupParticipants(participants)
	admitted.Request, admitted.Command = exact.Address.Remainder, exact.Address.Command
	if exact.Address.Bare {
		admitted.LocalReply = selected.DisplayName + " here."
	} else if greeting {
		admitted.LocalReply = s.greetingReply(responseEnvelope(selected))
	}
	admitted.Envelope = responseEnvelope(selected)
	return admitted, nil
}

// RouteHandoff classifies a delivered response without loading or selecting a
// persona. The pipeline may discard the proposal when user speech arrives.
func (s *Selector) routeNamedHandoff(ctx context.Context, response brain.Turn, visited []string) (pipeline.HandoffProposal, bool, error) {
	var proposal pipeline.HandoffProposal
	if response.PersonaID == "" || response.PersonaID != s.Snapshot().PersonaID || strings.TrimSpace(response.Content) == "" {
		return proposal, false, nil
	}
	roster, participants, err := s.conversationRoster()
	if err != nil {
		return proposal, false, wrapIdentityRefusalFailure(ctx, "route_handoff", err)
	}
	if len(participants) < 2 {
		return proposal, false, nil
	}
	address, err := s.resolveAddress(ctx, response.Content, "agent", response.PersonaDisplayName, roster, participants)
	if err != nil {
		if isIdentityRefusal(err) {
			return proposal, false, wrapIdentityRefusalFailure(ctx, "route_handoff", err)
		}
		return proposal, false, classifyAdmissionFailure(ctx, "route_handoff", err)
	}
	named := append([]string{address.PersonaID}, address.Invited...)
	var targets []string
	for _, id := range named {
		if id != "" && id != response.PersonaID && !slices.Contains(visited, id) {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return proposal, false, nil
	}
	proposal.PersonaID = targets[0]
	proposal.DisplayName = roster.DisplayName(targets[0])
	for _, id := range targets[1:] {
		proposal.Invited = append(proposal.Invited, pipeline.HandoffInvite{PersonaID: id, DisplayName: roster.DisplayName(id), InvitedBy: response.ResponseID})
	}
	proposal.Commit = s.commitHandoff(s.Snapshot(), targets[0], "", func(ctx context.Context, current *summon.Roster) error {
		if address.Reason != "explicit-mention" {
			return nil
		}
		exact := current.ResolveAgentMention(response.Content)
		if exact.Kind != summon.ExactControlValid || !slices.Equal(append([]string{exact.Address.PersonaID}, exact.Address.Invited...), named) {
			// The reply no longer names the personas it was read as inviting,
			// so none of them is asked.
			return &pipeline.InviteRefusal{Reason: "the personas the reply mentions changed while it was being prepared"}
		}
		return nil
	})
	return proposal, true, nil
}

// InviteHandoff proposes the floor for a persona an earlier reply in this turn
// invited after another. The selection has moved on since that reply, so the
// proposal belongs to the persona who has just answered. A provider that cannot
// pass the conversation on refuses every waiting invitee at once.
func (s *Selector) InviteHandoff(ctx context.Context, response brain.Turn, invite pipeline.HandoffInvite, visited []string) (pipeline.HandoffProposal, bool, error) {
	var proposal pipeline.HandoffProposal
	if invite.PersonaID == "" || slices.Contains(visited, invite.PersonaID) {
		return proposal, false, nil
	}
	if err := ctx.Err(); err != nil {
		return proposal, false, classifyAdmissionFailure(ctx, "route_handoff", err)
	}
	current := s.Snapshot()
	if response.PersonaID == "" {
		return proposal, false, nil
	}
	if response.PersonaID != current.PersonaID {
		return proposal, false, &pipeline.InviteRefusal{Reason: selectionMoved}
	}
	if !s.repliesHandOff() {
		return proposal, false, &pipeline.InviteRefusal{Reason: current.DisplayName + " cannot pass the conversation to another persona"}
	}
	proposal.PersonaID = invite.PersonaID
	proposal.Commit = s.commitHandoff(current, invite.PersonaID, invite.InvitedBy, nil)
	return proposal, true, nil
}

// selectionMoved is why no invited persona is asked once the conversation is no
// longer with the persona whose reply invited them.
const selectionMoved = "the persona selection changed before their turn"

// commitHandoff selects personaID at the response boundary, provided the
// selection is still expected. check runs against the current roster first.
// invitedBy is the reply that invited a waiting invitee, where its share of
// the conversation starts.
func (s *Selector) commitHandoff(expected events.PersonaSelection, personaID, invitedBy string, check func(context.Context, *summon.Roster) error) func(context.Context) (pipeline.ResponseAdmission, error) {
	return func(ctx context.Context) (pipeline.ResponseAdmission, error) {
		s.transitionMu.Lock()
		defer s.transitionMu.Unlock()
		if s.Snapshot() != expected {
			return pipeline.ResponseAdmission{}, &pipeline.InviteRefusal{Reason: selectionMoved}
		}
		// Resolve the roster before durable publication. No fallible Admit or
		// profile reload may follow a successful selection and strand its target.
		currentRoster, participants, err := s.conversationRoster()
		if err != nil {
			return pipeline.ResponseAdmission{}, wrapIdentityRefusalFailure(ctx, "route_handoff", err)
		}
		if check != nil {
			if err := check(ctx, currentRoster); err != nil {
				return pipeline.ResponseAdmission{}, err
			}
		}
		selected, err := s.selectAtBoundary(withInvitingReply(ctx, invitedBy), personaID, "agent-handoff")
		if err != nil {
			return pipeline.ResponseAdmission{}, wrapIdentityRefusalFailure(ctx, "route_handoff", err)
		}
		s.setGroupParticipants(participants)
		return pipeline.ResponseAdmission{Envelope: responseEnvelope(selected), AgentMentionName: agentMentionName(currentRoster)}, nil
	}
}

// repliesHandOff reports whether a reply may hand the conversation to another
// persona. An agent never moves the conversation off a provider that could
// not take it back, nor out of a conversation that stays with its persona.
func (s *Selector) repliesHandOff() bool {
	live := s.opts.Pipeline.Brain
	return brain.KeepsSharedHistory(live) || (brain.KeepsPersonaSession(live) && !s.fixedResponder())
}

// groupParticipation reports whether group routing applies. Group turns are
// projected into each member, so the live provider must keep shared history.
func (s *Selector) groupParticipation() bool {
	return s.opts.Base.GroupParticipationEnabled && brain.KeepsSharedHistory(s.opts.Pipeline.Brain)
}

// RouteHandoff gives explicit handoffs priority over optional open participation.
func (s *Selector) RouteHandoff(ctx context.Context, response brain.Turn, visited []string) (pipeline.HandoffProposal, bool, error) {
	if !s.repliesHandOff() {
		return pipeline.HandoffProposal{}, false, nil
	}
	s.groupMu.Lock()
	active := s.group != nil
	s.groupMu.Unlock()
	if active {
		return s.nextDiscussion(ctx, response)
	}
	proposal, ok, err := s.routeNamedHandoff(ctx, response, visited)
	if err != nil || ok {
		return proposal, ok, err
	}
	if s.groupParticipation() && s.Participation().Mode == "open" && len(visited) == 1 {
		return s.proposeOpen(ctx, response)
	}
	return proposal, false, nil
}
