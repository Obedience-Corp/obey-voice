package personaruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type groupMember struct {
	id   string
	cfg  *config.Config
	hash string
}
type groupRun struct {
	scope        work.WorkScope
	state        *discussion.State
	policy       discussion.Decider
	participants []discussion.Participant
	members      map[string]groupMember
	rosterHash   string
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Selector) Participation() discussion.Settings {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return s.participation.Clone()
}
func (s *Selector) DiscussionSnapshot() *discussion.Snapshot {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if s.group == nil {
		return nil
	}
	snap := publicGroupSnapshot(s.group)
	return &snap
}
func (s *Selector) EndDiscussion(reason string) {
	s.groupMu.Lock()
	g := s.group
	s.group = nil
	s.groupMu.Unlock()
	if g != nil && g.state.Snapshot().Reason == "" {
		g.state.End(reason)
		s.emitDiscussion(g)
	}
}
func (s *Selector) emitDiscussion(g *groupRun) {
	if s.opts.Pipeline.Events != nil {
		selected := s.Snapshot()
		s.opts.Pipeline.Events.Emit(events.DiscussionUpdated{SessionID: selected.SessionID, ConversationEpoch: selected.ConversationEpoch, Snapshot: publicGroupSnapshot(g)})
	}
}
func (s *Selector) SetParticipation(ctx context.Context, settings discussion.Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.opts.Base.GroupParticipationEnabled {
		return errors.New("enable group_participation_enabled for a new conversation first")
	}
	for _, id := range settings.Members {
		if _, err := s.opts.Catalog.ResolveBinding(s.opts.Base, id); err != nil {
			return err
		}
	}
	if s.opts.PersistParticipation == nil {
		return errors.New("this host cannot persist participation settings")
	}
	if err := s.opts.PersistParticipation(settings); err != nil {
		if !session.WriteCommitted(err) {
			return err
		}
		s.warnPersistence(err)
	}
	s.EndDiscussion("superseded")
	s.groupMu.Lock()
	s.participation = settings.Clone()
	s.groupMu.Unlock()
	return nil
}
func (s *Selector) groupParticipants() ([]discussion.Participant, map[string]string, []persona.CatalogIssue, error) {
	profiles, issues, err := s.opts.Catalog.ListWithIssues()
	if err != nil {
		return nil, nil, nil, err
	}
	settings := s.Participation()
	current := s.Snapshot().PersonaID
	slices.SortFunc(profiles, func(a, b *persona.Profile) int {
		if a.ID == current {
			return -1
		}
		if b.ID == current {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	participants := []discussion.Participant{}
	ids := map[string]string{}
	for _, p := range profiles {
		if p.Inactive || (settings.Members != nil && !slices.Contains(settings.Members, p.ID)) {
			continue
		}
		key := fmt.Sprintf("p%d", len(participants))
		ids[key] = p.ID
		aliases := slices.Clone(p.Aliases)
		if words := strings.Fields(p.DisplayName); len(words) > 1 {
			aliases = append(aliases, words[0])
		}
		if p.ID == s.opts.Initial.PersonaID {
			aliases = append(aliases, s.opts.Base.NameSummonAliases...)
		}
		participants = append(participants, discussion.Participant{Key: key, Name: p.DisplayName, Aliases: aliases, Card: p.ParticipationDescription})
	}
	return participants, ids, issues, nil
}
func (s *Selector) discussionPolicy() (discussion.Decider, error) {
	if s.opts.DiscussionDecider != nil {
		return s.opts.DiscussionDecider, nil
	}
	provider, ok := s.opts.Pipeline.Brain.(brain.DiscussionProvider)
	if !ok {
		return nil, errors.New("current provider does not support local group participation")
	}
	return provider.DiscussionDecider()
}
func (s *Selector) routeAudience(ctx context.Context, input string) (pipeline.ResponseAdmission, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	parts, ids, issues, err := s.groupParticipants()
	if err != nil {
		return admitted, err
	}
	if len(parts) > 64 {
		return admitted, errors.New("choose a smaller conversation roster with /participation")
	}
	if len(parts) > discussion.MaxMembers {
		for i := range parts {
			parts[i].Card = ""
		}
	}
	if len(parts) == 0 {
		return admitted, errors.New("no enabled conversation participants")
	}
	policy, err := s.discussionPolicy()
	if err != nil {
		return admitted, err
	}
	expectedSelection := s.Snapshot()
	expectedSettings := digest(s.Participation())
	expectedHistory := digest(s.opts.Pipeline.Brain.History())
	current := ""
	for k, id := range ids {
		if id == s.Snapshot().PersonaID {
			current = k
		}
	}
	request := discussion.AudienceRequest{Participants: parts, Current: current, Speaker: s.humanSpeakerName(""), Latest: input, AcousticName: admission.AcousticName(ctx)}
	turns := s.opts.Pipeline.Brain.History()
	s.groupMu.Lock()
	if s.recentAudienceScope == s.opts.Pipeline.ConversationScope() && len(turns)-s.recentAudienceAt <= 8 {
		for _, id := range s.recentAudience {
			for key, actual := range ids {
				if actual == id {
					request.RecentAudience = append(request.RecentAudience, key)
				}
			}
		}
	}
	s.groupMu.Unlock()
	slices.Sort(request.RecentAudience)
	for _, t := range turns {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		name, account := s.humanSpeakerName(t.Speaker), "user"
		if t.Role == "assistant" {
			name, account = t.PersonaDisplayName, "agent"
		}
		request.History = append(request.History, discussion.Message{Name: name, Account: account, Text: clipDiscussion(t.Content)})
	}
	if len(request.History) > 8 {
		request.History = request.History[len(request.History)-8:]
	}
	a, err := policy.Audience(ctx, request)
	if err != nil {
		return admitted, classifyAdmissionFailure(ctx, "group_audience", err)
	}
	if ctx.Err() != nil {
		return admitted, ctx.Err()
	}
	if s.Snapshot() != expectedSelection || digest(s.Participation()) != expectedSettings || digest(s.opts.Pipeline.Brain.History()) != expectedHistory {
		return admitted, discussion.ErrStale
	}
	if err = a.Validate(parts); err != nil {
		return admitted, wrapInvalidClassifierOutput("group_audience", err)
	}
	switch a.Kind {
	case "current":
		admitted.Envelope, err = s.Admit(ctx)
		return admitted, err
	case "direct":
		selected, selectErr := s.Select(ctx, ids[a.Members[0]], "semantic-address")
		if selectErr != nil {
			return admitted, selectErr
		}
		admitted.Envelope = responseEnvelope(selected)
		return admitted, nil
	case "clarify":
		if err := s.recordGroupInput(ctx, input); err != nil {
			return admitted, err
		}
		return admitted, fmt.Errorf("group audience is %s (%s); name the intended agents", a.Reason, strings.Join(a.Unresolved, ", "))
	}
	if err := s.recordGroupInput(ctx, input); err != nil {
		return admitted, err
	}
	if s.Participation().Mode == "off" {
		return admitted, errors.New("group participation is off; use /participation invited")
	}
	if len(a.Members) > discussion.MaxMembers {
		return admitted, errors.New("group discussion supports at most eight invited personas; choose a smaller group")
	}
	if len(issues) > 0 {
		return admitted, errors.New("repair unavailable persona profiles before a group discussion")
	}
	if len([]rune(input)) > 4096 {
		return admitted, errors.New("group request exceeds 4096 characters")
	}
	// Retain roster order, not model-generated turn order.
	selectedParts := []discussion.Participant{}
	members := map[string]groupMember{}
	for _, p := range parts {
		if !slices.Contains(a.Members, p.Key) {
			continue
		}
		binding, loadErr := s.opts.Catalog.ResolveBinding(s.opts.Base, ids[p.Key])
		if loadErr != nil {
			return admitted, loadErr
		}
		cfg := binding.Config()
		if cfg.BrainProvider != "ollama" {
			return admitted, fmt.Errorf("persona %s needs local Ollama for group participation", p.Name)
		}
		hash, loadErr := brain.PersonaPromptHash(cfg)
		if loadErr != nil {
			return admitted, loadErr
		}
		members[p.Key] = groupMember{id: ids[p.Key], cfg: cfg, hash: hash}
		selectedParts = append(selectedParts, p)
	}
	keys := []string{}
	for _, p := range selectedParts {
		keys = append(keys, p.Key)
	}
	g := &groupRun{scope: s.opts.Pipeline.ConversationScope(), state: discussion.New(ctx, input, a.Coverage, "invited", keys), policy: policy, participants: selectedParts, members: members}
	g.rosterHash, err = s.groupFingerprint(g)
	if err != nil {
		return admitted, err
	}
	s.groupMu.Lock()
	s.group = g
	s.recentAudience = nil
	for _, part := range selectedParts {
		s.recentAudience = append(s.recentAudience, members[part.Key].id)
	}
	s.recentAudienceAt = len(s.opts.Pipeline.Brain.History())
	s.recentAudienceScope = s.opts.Pipeline.ConversationScope()
	s.groupMu.Unlock()
	s.emitDiscussion(g)
	proposal, ok, err := s.proposeDiscussion(ctx, g, true)
	if err != nil {
		s.EndDiscussion("failed")
		return admitted, err
	}
	if !ok {
		admitted.Skip = true
		return admitted, nil
	}
	return proposal.Commit(ctx)
}
func clipDiscussion(text string) string {
	r := []rune(brain.CleanHistoricalAssistantText(text))
	if len(r) > 1000 {
		r = r[:1000]
	}
	return string(r)
}
func (s *Selector) recordGroupInput(ctx context.Context, input string) error {
	provider, ok := s.opts.Pipeline.Brain.(brain.ContinuityTransferProvider)
	if !ok {
		return errors.New("group discussion requires shared continuity")
	}
	before := provider.ContinuitySnapshot()
	next := before
	t := brain.Turn{Role: "user", Content: input, Speaker: s.opts.Pipeline.CurrentSpeakerID(), At: time.Now(), Attachments: chatmedia.Input(ctx)}
	next.Canonical = append(slices.Clone(before.Canonical), t)
	working := before.Working
	if working == nil {
		working = before.Canonical
	}
	next.Working = append(slices.Clone(working), t)
	if err := provider.ImportContinuity(next); err != nil {
		return err
	}
	if err := s.opts.Persist(next.Canonical, s.Snapshot()); err != nil {
		if session.WriteCommitted(err) {
			s.warnPersistence(err)
			return nil
		}
		return errors.Join(err, provider.ImportContinuity(before))
	}
	return nil
}
func (s *Selector) groupFingerprint(g *groupRun) (string, error) {
	profiles, issues, err := s.opts.Catalog.ListWithIssues()
	if err != nil {
		return "", err
	}
	if len(issues) > 0 {
		return "", errors.New("persona catalog changed or is unavailable")
	}
	hashes := []string{digest(profiles), digest(s.Participation())}
	for _, p := range g.participants {
		m := g.members[p.Key]
		binding, err := s.opts.Catalog.ResolveBinding(s.opts.Base, m.id)
		if err != nil {
			return "", err
		}
		cfg := binding.Config()
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return "", err
		}
		if hash != m.hash {
			return "", errors.New("discussion persona prompt changed")
		}
		hashes = append(hashes, hash)
	}
	return digest(hashes), nil
}

// RestoreParticipation loads stable session settings without restarting any discussion.
func (s *Selector) RestoreParticipation(saved *discussion.Settings) error {
	settings := discussion.DefaultSettings()
	if s.opts.Base.GroupParticipationMode != "" {
		settings.Mode = s.opts.Base.GroupParticipationMode
	}
	if saved != nil {
		settings = saved.Clone()
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	s.EndDiscussion("superseded")
	s.groupMu.Lock()
	s.participation = settings
	s.groupMu.Unlock()
	return nil
}

func publicGroupSnapshot(g *groupRun) discussion.Snapshot {
	snap := g.state.Snapshot()
	snap.Labels = map[string]string{}
	for _, p := range g.participants {
		snap.Labels[g.members[p.Key].id] = p.Name
	}
	for i, key := range snap.Members {
		snap.Members[i] = g.members[key].id
	}
	for i, key := range snap.Uncovered {
		snap.Uncovered[i] = g.members[key].id
	}
	if snap.Candidate != "" {
		snap.Candidate = g.members[snap.Candidate].id
	}
	return snap
}
