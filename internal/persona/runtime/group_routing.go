package personaruntime

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
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

func (s *Selector) resolveAddress(ctx context.Context, text, account, speaker string, roster *summon.Roster, participants []brain.GroupParticipant) (summon.Address, error) {
	if account == "agent" && !roster.Mentions(text) {
		return summon.Address{Remainder: text}, nil
	}
	resolver := s.opts.AddressResolver
	if resolver == nil {
		resolver, _ = s.opts.Pipeline.Brain.(brain.AddresseeResolver)
	}
	if resolver == nil {
		// Compatibility for hosts/providers without a semantic classifier. Agent
		// handoffs require semantic support; legacy grammar cannot infer intent.
		if account == "agent" {
			return summon.Address{Remainder: text}, nil
		}
		return roster.Resolve(text)
	}
	request := brain.AddressRequest{Participants: participants, Current: s.Snapshot().DisplayName, Speaker: speaker, Account: account, Text: text}
	turns := s.opts.Pipeline.Brain.History()
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
	decision, err := resolver.ResolveAddressee(ctx, request)
	if err != nil {
		return summon.Address{}, err
	}
	if err := ctx.Err(); err != nil {
		return summon.Address{}, err
	}
	switch decision.Kind {
	case "none":
		return summon.Address{Remainder: text}, nil
	case "ambiguous":
		// Lookup preserves the existing precise collision error when possible.
		if _, err := roster.Lookup(decision.Name); err != nil {
			if _, ok := err.(*summon.AmbiguousTargetError); ok {
				return summon.Address{}, err
			}
		}
		return summon.Address{}, fmt.Errorf("conversation addressee is ambiguous; address one persona by a unique name")
	case "address":
		address, err := roster.Lookup(decision.Name)
		if err != nil {
			return summon.Address{}, err
		}
		// Keep the user's words exactly, including the name at any position.
		address.Remainder, address.Bare = text, decision.Bare && account == "user"
		return address, nil
	default:
		return summon.Address{}, fmt.Errorf("invalid conversation addressee decision %q", decision.Kind)
	}
}

func (s *Selector) routeExactControl(ctx context.Context, input string, exact summon.ExactControl) (pipeline.ResponseAdmission, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	var participants []brain.GroupParticipant
	selected, err := s.selectAtBoundaryWithCommitCheck(ctx, exact.Address.PersonaID, exact.Address.Reason, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		roster, refreshedParticipants, err := s.conversationRoster()
		if err != nil {
			return err
		}
		revalidated := roster.ResolveExactControl(input)
		switch revalidated.Kind {
		case summon.ExactControlValid:
			if revalidated.Address.PersonaID != exact.Address.PersonaID {
				return fmt.Errorf("exact identity control changed target while preparing")
			}
		case summon.ExactControlInvalid:
			if revalidated.Err != nil {
				return revalidated.Err
			}
			return fmt.Errorf("exact identity control became invalid")
		default:
			return fmt.Errorf("exact identity control no longer matches current roster")
		}
		participants = refreshedParticipants
		return nil
	})
	if err != nil {
		return admitted, err
	}
	s.setGroupParticipants(participants)
	admitted.Request = ""
	admitted.LocalReply = selected.DisplayName + " here."
	admitted.Envelope = responseEnvelope(selected)
	return admitted, nil
}

// RouteHandoff classifies a delivered response without loading or selecting a
// persona. The pipeline may discard the proposal when user speech arrives.
func (s *Selector) RouteHandoff(ctx context.Context, response brain.Turn, visited []string) (pipeline.HandoffProposal, bool, error) {
	var proposal pipeline.HandoffProposal
	if response.PersonaID == "" || response.PersonaID != s.Snapshot().PersonaID || strings.TrimSpace(response.Content) == "" {
		return proposal, false, nil
	}
	roster, participants, err := s.conversationRoster()
	if err != nil {
		return proposal, false, err
	}
	if len(participants) < 2 {
		return proposal, false, nil
	}
	address, err := s.resolveAddress(ctx, response.Content, "agent", response.PersonaDisplayName, roster, participants)
	if err != nil {
		return proposal, false, err
	}
	if address.PersonaID == "" || address.PersonaID == response.PersonaID || slices.Contains(visited, address.PersonaID) {
		return proposal, false, nil
	}
	expected := s.Snapshot()
	proposal.PersonaID = address.PersonaID
	proposal.Commit = func(ctx context.Context) (pipeline.ResponseAdmission, error) {
		s.transitionMu.Lock()
		defer s.transitionMu.Unlock()
		if s.Snapshot() != expected {
			return pipeline.ResponseAdmission{}, fmt.Errorf("agent handoff belongs to a changed persona selection")
		}
		// Resolve the roster before durable publication. No fallible Admit or
		// profile reload may follow a successful selection and strand its target.
		_, participants, err := s.conversationRoster()
		if err != nil {
			return pipeline.ResponseAdmission{}, err
		}
		selected, err := s.selectAtBoundary(ctx, address.PersonaID, "agent-handoff")
		if err != nil {
			return pipeline.ResponseAdmission{}, err
		}
		s.setGroupParticipants(participants)
		return pipeline.ResponseAdmission{Envelope: responseEnvelope(selected)}, nil
	}
	return proposal, true, nil
}
