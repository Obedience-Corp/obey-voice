package personaruntime

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func (s *Selector) proposeOpen(ctx context.Context, response brain.Turn) (pipeline.HandoffProposal, bool, error) {
	empty := pipeline.HandoffProposal{}
	p := s.opts.Pipeline
	if p.BackgroundPending() || p.HasCapturedInput() {
		return empty, false, nil
	}
	parts, ids, issues, err := s.groupParticipants()
	if err != nil {
		return empty, false, err
	}
	if len(parts) < 2 || len(parts) > discussion.MaxMembers || len(issues) > 0 {
		return empty, false, nil
	}
	policy, err := s.discussionPolicy()
	if err != nil {
		return empty, false, err
	}
	original := ""
	turns := p.Brain.History()
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			original = turns[i].Content
			break
		}
	}
	if original == "" || len([]rune(original)) > 4096 {
		return empty, false, nil
	}
	keys := []string{}
	members := map[string]groupMember{}
	seedKey := ""
	for _, part := range parts {
		id := ids[part.Key]
		if id == response.PersonaID {
			seedKey = part.Key
		}
		binding, err := s.opts.Catalog.ResolveBinding(s.opts.Base, id)
		if err != nil {
			return empty, false, err
		}
		cfg := binding.Config()
		if cfg.BrainProvider != "ollama" {
			return empty, false, nil
		}
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return empty, false, err
		}
		members[part.Key] = groupMember{id: id, cfg: cfg, hash: hash}
		keys = append(keys, part.Key)
	}
	if seedKey == "" {
		return empty, false, fmt.Errorf("open participation has no current speaker")
	}
	g := &groupRun{scope: s.opts.Pipeline.ConversationScope(), state: discussion.New(pipeline.ContinuationOwner(ctx), original, "relevant", "open", keys), policy: policy, participants: parts, members: members}
	g.state.Seed(discussion.Message{Key: seedKey, Name: response.PersonaDisplayName, Account: "agent", ResponseID: response.ResponseID, Text: clipDiscussion(response.Content)})
	g.rosterHash, err = s.groupFingerprint(g)
	if err != nil {
		return empty, false, err
	}
	s.groupMu.Lock()
	s.group = g
	s.groupMu.Unlock()
	proposal, ok, err := s.proposeDiscussion(ctx, g, false)
	if err != nil {
		s.EndDiscussion("failed")
		return empty, false, err
	}
	if ok {
		proposal.OpenAddition = true
	}
	return proposal, ok, nil
}
