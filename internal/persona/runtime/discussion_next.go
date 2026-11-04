package personaruntime

import (
	"context"
	"errors"
	"slices"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// RecordDiscussionResponse commits delivery coverage only after canonical persistence succeeds.
func (s *Selector) RecordDiscussionResponse(ctx context.Context, response brain.Turn) error {
	s.groupMu.Lock()
	g := s.group
	s.groupMu.Unlock()
	if g == nil {
		return discussion.ErrStale
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snap := g.state.Snapshot()
	m, ok := g.members[snap.Candidate]
	if !ok || m.id != response.PersonaID || response.ResponseID == "" {
		s.EndDiscussion("failed")
		return errors.New("discussion completion has mismatched identity")
	}
	if err := s.persistContexts(s.opts.Pipeline.Brain.History(), s.Snapshot(), s.ContextSnapshots()); err != nil {
		if !session.WriteCommitted(err) {
			s.EndDiscussion("failed")
			return err
		}
		s.warnPersistence(err)
	}
	g.state.Delivered(discussion.Message{Key: snap.Candidate, Name: response.PersonaDisplayName, Account: "agent", ResponseID: response.ResponseID, Text: clipDiscussion(response.Content)})
	s.emitDiscussion(g)
	return nil
}
func (s *Selector) nextDiscussion(ctx context.Context, response brain.Turn) (pipeline.HandoffProposal, bool, error) {
	s.groupMu.Lock()
	g := s.group
	s.groupMu.Unlock()
	if g == nil {
		return pipeline.HandoffProposal{}, false, nil
	}
	snap := g.state.Snapshot()
	if s.opts.Pipeline.BackgroundPending() {
		s.EndDiscussion("work_ready")
		return pipeline.HandoffProposal{}, false, nil
	}
	if snap.Mode == "open" {
		s.EndDiscussion("open_complete")
		return pipeline.HandoffProposal{}, false, nil
	}
	if len(g.members) == 1 {
		s.EndDiscussion("yielded")
		return pipeline.HandoffProposal{}, false, nil
	}
	return s.proposeDiscussion(ctx, g, false)
}
func (s *Selector) proposeDiscussion(ctx context.Context, g *groupRun, initial bool) (pipeline.HandoffProposal, bool, error) {
	ctx, stop := discussion.Bind(ctx, g.state)
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return pipeline.HandoffProposal{}, false, err
		}
		contextData, key, err := g.state.NextContext(g.participants)
		if errors.Is(err, discussion.ErrBudget) {
			s.EndDiscussion("budget_exhausted")
			return pipeline.HandoffProposal{}, false, nil
		}
		if err != nil {
			return pipeline.HandoffProposal{}, false, err
		}
		fingerprint, err := s.groupFingerprint(g)
		if err != nil || fingerprint != g.rosterHash {
			s.EndDiscussion("superseded")
			return pipeline.HandoffProposal{}, false, errors.New("discussion roster or prompt changed")
		}
		if key == "" {
			if len(contextData.Eligible) == 0 {
				s.EndDiscussion("yielded")
				return pipeline.HandoffProposal{}, false, nil
			}
			g.state.Phase("choosing", "")
			s.emitDiscussion(g)
			next, err := g.policy.Next(ctx, contextData)
			if err != nil {
				return pipeline.HandoffProposal{}, false, err
			}
			if err := next.Validate(contextData); err != nil {
				return pipeline.HandoffProposal{}, false, err
			}
			if next.Kind == "yield" {
				s.EndDiscussion("yielded")
				return pipeline.HandoffProposal{}, false, nil
			}
			key = next.Member
		}
		member := g.members[key]
		expected := s.Snapshot()
		historyHash := digest(s.opts.Pipeline.Brain.History())
		provider, ok := s.opts.Pipeline.Brain.(brain.ContinuityTransferProvider)
		if !ok {
			return pipeline.HandoffProposal{}, false, errors.New("shared continuity unavailable")
		}
		persona, err := brain.DiscussionPersona(member.cfg, key, s.ContextSnapshots()[member.id].Reconcile(provider.ContinuitySnapshot().Canonical).Working)
		if err != nil {
			return pipeline.HandoffProposal{}, false, err
		}
		g.state.Phase("checking", key)
		s.emitDiscussion(g)
		ticket := g.state.Ticket()
		contribution, err := g.policy.Check(ctx, contextData, persona)
		if err != nil {
			return pipeline.HandoffProposal{}, false, err
		}
		if !g.state.Valid(ticket) || s.Snapshot() != expected || digest(s.opts.Pipeline.Brain.History()) != historyHash {
			return pipeline.HandoffProposal{}, false, discussion.ErrStale
		}
		if err := contribution.Validate(contextData, key, g.state.Returning(key)); err != nil {
			return pipeline.HandoffProposal{}, false, err
		}
		if contribution.Kind == "pass" {
			if g.state.Passed(key) {
				continue
			}
			s.EndDiscussion("no_contribution")
			return pipeline.HandoffProposal{}, false, nil
		}
		g.state.Phase("preparing", key)
		s.emitDiscussion(g)
		ticket = g.state.Ticket()
		proposal := pipeline.HandoffProposal{PersonaID: member.id, DiscussionID: g.state.ID()}
		proposal.Commit = func(commitCtx context.Context) (pipeline.ResponseAdmission, error) {
			s.transitionMu.Lock()
			defer s.transitionMu.Unlock()
			verify := func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !g.state.Valid(ticket) || s.Snapshot() != expected || s.opts.Pipeline.ConversationScope() != g.scope || s.opts.Pipeline.ConversationRetired() || digest(s.opts.Pipeline.Brain.History()) != historyHash || s.opts.Pipeline.HasCapturedInput() {
					return discussion.ErrStale
				}
				fingerprint, err := s.groupFingerprint(g)
				if err != nil {
					return err
				}
				if fingerprint != g.rosterHash {
					return discussion.ErrStale
				}
				return nil
			}
			if err := verify(commitCtx); err != nil {
				return pipeline.ResponseAdmission{}, err
			}
			roster, _, err := s.conversationRoster()
			if err != nil {
				return pipeline.ResponseAdmission{}, err
			}
			selected, err := s.selectAtBoundaryWithCommitCheck(commitCtx, member.id, "group-discussion", verify)
			if err != nil {
				return pipeline.ResponseAdmission{}, err
			}
			g.state.Phase("responding", key)
			s.emitDiscussion(g)
			envelope := responseEnvelope(selected)
			envelope.DiscussionID = g.state.ID()
			directive := &discussion.Directive{ID: g.state.ID(), Initial: initial, Intent: contribution.Intent, Original: contextData.Original, InputAlreadyRecorded: true, State: g.state}
			outputTicket := g.state.Ticket()
			directive.BeforeOutput = func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !g.state.Valid(outputTicket) || s.Snapshot() != selected || s.opts.Pipeline.ConversationScope() != g.scope || s.opts.Pipeline.ConversationRetired() || s.opts.Pipeline.HasCapturedInput() {
					return discussion.ErrStale
				}
				fingerprint, err := s.groupFingerprint(g)
				if err != nil {
					return err
				}
				if fingerprint != g.rosterHash {
					return discussion.ErrStale
				}
				return nil
			}
			return pipeline.ResponseAdmission{Envelope: envelope, Discussion: directive, Request: contextData.Original, AgentMentionControl: agentMentionControl(roster)}, nil
		}
		return proposal, true, nil
	}
}

// MemberIDs returns only public selectable IDs; it never expands a discussion.
func (s *Selector) MemberIDs() []string {
	settings := s.Participation()
	return slices.Clone(settings.Members)
}
