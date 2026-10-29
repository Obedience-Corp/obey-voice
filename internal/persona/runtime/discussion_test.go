package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type groupPolicy struct {
	audience          func(context.Context, discussion.AudienceRequest) (discussion.Audience, error)
	next              func(context.Context, discussion.Context) (discussion.Next, error)
	check             func(context.Context, discussion.Context, discussion.Persona) (discussion.Contribution, error)
	checks, nextCalls int
}

func (p *groupPolicy) Audience(ctx context.Context, r discussion.AudienceRequest) (discussion.Audience, error) {
	return p.audience(ctx, r)
}
func (p *groupPolicy) Next(ctx context.Context, r discussion.Context) (discussion.Next, error) {
	p.nextCalls++
	if err := discussion.ReserveRequest(ctx); err != nil {
		return discussion.Next{}, err
	}
	return p.next(ctx, r)
}
func (p *groupPolicy) Check(ctx context.Context, r discussion.Context, persona discussion.Persona) (discussion.Contribution, error) {
	p.checks++
	if err := discussion.ReserveRequest(ctx); err != nil {
		return discussion.Contribution{}, err
	}
	return p.check(ctx, r, persona)
}

type groupReplyBrain struct {
	*selectorTestBrain
	name    string
	options *[]brain.StreamOptions
}

func (b *groupReplyBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	*b.options = append(*b.options, opts)
	if err := discussion.ReserveRequest(ctx); err != nil {
		return nil, err
	}
	response := "A useful point from " + b.name + "."
	b.mu.Lock()
	turn := brain.Turn{Role: "assistant", Content: response, PersonaID: opts.Identity.PersonaID, PersonaDisplayName: opts.Identity.PersonaDisplayName, ResponseID: opts.Identity.ResponseID, PromptHash: opts.Identity.PromptHash, At: time.Now()}
	b.history = append(b.history, turn)
	b.working = append(b.working, turn)
	b.mu.Unlock()
	chunks := make(chan string, 1)
	chunks <- response
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func groupFixture(t *testing.T) (*selectorTestFixture, *groupPolicy, *[]brain.StreamOptions) {
	t.Helper()
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("samantha", "Samantha", "samantha"))
	f.selector.opts.Base.GroupParticipationEnabled = true
	options := new([]brain.StreamOptions)
	f.pipeline.Brain = &groupReplyBrain{selectorTestBrain: f.old, name: "Jarvis", options: options}
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return &groupReplyBrain{selectorTestBrain: newSelectorTestBrain(hash, nil), name: cfg.AgentName, options: options}, nil
	}
	policy := &groupPolicy{}
	policy.audience = func(_ context.Context, r discussion.AudienceRequest) (discussion.Audience, error) {
		keys := []string{}
		for _, p := range r.Participants {
			keys = append(keys, p.Key)
		}
		return discussion.Audience{Kind: "group", Members: keys, Coverage: "all", Reason: "explicit"}, nil
	}
	policy.next = func(context.Context, discussion.Context) (discussion.Next, error) {
		return discussion.Next{Kind: "yield", Reason: "no_contribution"}, nil
	}
	policy.check = func(_ context.Context, c discussion.Context, p discussion.Persona) (discussion.Contribution, error) {
		return discussion.Contribution{Kind: "speak", Reason: "perspective", Intent: "Offer my own view"}, nil
	}
	f.selector.opts.DiscussionDecider = policy
	f.pipeline.Events = events.NewBus()
	f.pipeline.AdmitInput = f.selector.Route
	f.pipeline.RouteHandoff = f.selector.RouteHandoff
	f.pipeline.EndDiscussion = f.selector.EndDiscussion
	f.pipeline.CompleteDiscussion = f.selector.RecordDiscussionResponse
	t.Cleanup(func() { f.close(t) })
	return f, policy, options
}
func TestGroupDiscussionPipelineReturnsToEarlierSpeaker(t *testing.T) {
	f, policy, options := groupFixture(t)
	policy.next = func(_ context.Context, c discussion.Context) (discussion.Next, error) {
		if len(c.Delivered) == 2 {
			return discussion.Next{Kind: "select", Member: "p0", Reason: "answer"}, nil
		}
		return discussion.Next{Kind: "yield", Reason: "no_contribution"}, nil
	}
	policy.check = func(_ context.Context, c discussion.Context, p discussion.Persona) (discussion.Contribution, error) {
		result := discussion.Contribution{Kind: "speak", Reason: "perspective", Intent: "Offer a useful view"}
		if len(c.Delivered) >= 2 {
			result.Reason = "answer"
			result.References = []string{c.Delivered[len(c.Delivered)-1].ResponseID}
		}
		return result, nil
	}
	input := "What does everyone think about this?"
	if err := f.pipeline.RunTurnTextMode(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(*options) != 3 {
		t.Fatalf("reply calls=%d want 3", len(*options))
	}
	history := f.pipeline.Brain.History()
	speakers := []string{}
	human := 0
	for _, turn := range history {
		if turn.Role == "assistant" {
			speakers = append(speakers, turn.PersonaID)
			if turn.ResponseID == "" {
				t.Fatal("unattributed response")
			}
		}
		if turn.Role == "user" && turn.Content == input {
			human++
		}
	}
	if !reflect.DeepEqual(speakers, []string{"jarvis", "samantha", "jarvis"}) || human != 1 {
		t.Fatalf("speakers=%v human requests=%d history=%+v", speakers, human, history)
	}
	for _, opts := range *options {
		if opts.ToolsEnabled || opts.WorkerTiers != nil || opts.Discussion == nil {
			t.Fatalf("unsafe group options %+v", opts)
		}
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) != len(history) {
		t.Fatalf("persisted turns=%d current=%d", len(saved.Turns), len(history))
	}
	if f.selector.DiscussionSnapshot() != nil {
		t.Fatal("discussion did not end")
	}
}
func TestGroupAllPassPersistsInputWithoutSelectionOrVoice(t *testing.T) {
	f, policy, options := groupFixture(t)
	before := f.selector.Snapshot()
	policy.check = func(context.Context, discussion.Context, discussion.Persona) (discussion.Contribution, error) {
		return discussion.Contribution{Kind: "pass", Reason: "no_contribution"}, nil
	}
	input := "Everyone, thoughts?"
	if err := f.pipeline.RunTurnTextMode(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(*options) != 0 || !reflect.DeepEqual(before, f.selector.Snapshot()) {
		t.Fatal("private passes changed responder or generated speech")
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Turns[len(saved.Turns)-1].Content != input || policy.checks != 2 || policy.nextCalls != 0 {
		t.Fatalf("saved=%+v checks=%d next=%d", saved.Turns, policy.checks, policy.nextCalls)
	}
}
func TestGroupProposalRejectsChangedRosterAndHistory(t *testing.T) {
	for _, change := range []string{"roster", "history", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f, policy, _ := groupFixture(t)
			admitted, err := f.selector.Route(t.Context(), "Everyone, views?", false)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a completed attributed first reply at the existing owner boundary.
			turn := brain.Turn{Role: "assistant", Content: "First view.", PersonaID: admitted.Envelope.PersonaID, PersonaDisplayName: admitted.Envelope.DisplayName, ResponseID: admitted.Envelope.ResponseID}
			b := f.pipeline.Brain.(*groupReplyBrain)
			b.mu.Lock()
			b.history = append(b.history, turn)
			b.working = append(b.working, turn)
			b.mu.Unlock()
			before := f.selector.Snapshot()
			if err := f.selector.RecordDiscussionResponse(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			proposal, ok, err := f.selector.RouteHandoff(t.Context(), turn, []string{turn.PersonaID})
			if err != nil || !ok {
				t.Fatalf("proposal=%v err=%v", ok, err)
			}
			switch change {
			case "roster":
				p, err := f.catalog.Load("samantha")
				if err != nil {
					t.Fatal(err)
				}
				p.ParticipationDescription = "An edited perspective"
				writeTestProfile(t, f.catalog.Dir, *p)
			case "history":
				b.mu.Lock()
				b.history = append(b.history, brain.Turn{Role: "user", Content: "Newer input"})
				b.mu.Unlock()
			case "cancel":
				f.selector.EndDiscussion("interrupted")
			}
			_, err = proposal.Commit(t.Context())
			if err == nil {
				t.Fatal("stale proposal committed")
			}
			if !reflect.DeepEqual(before, f.selector.Snapshot()) {
				t.Fatal("stale proposal changed selection")
			}
			_ = policy
		})
	}
}
func TestGroupClassifierFailureAndUnknownKeyDoNotSelect(t *testing.T) {
	f, p, _ := groupFixture(t)
	before := f.selector.Snapshot()
	p.audience = func(context.Context, discussion.AudienceRequest) (discussion.Audience, error) {
		return discussion.Audience{Kind: "group", Members: []string{"p7"}, Coverage: "all", Reason: "explicit"}, nil
	}
	if _, err := f.selector.Route(t.Context(), "Everyone?", false); err == nil {
		t.Fatal("unknown key accepted")
	}
	if !reflect.DeepEqual(before, f.selector.Snapshot()) {
		t.Fatal("classifier changed selection")
	}
	p.audience = func(context.Context, discussion.AudienceRequest) (discussion.Audience, error) {
		return discussion.Audience{}, context.Canceled
	}
	if _, err := f.selector.Route(t.Context(), "Everyone?", false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestOpenParticipationAddsOneReplyAndNeverPolls(t *testing.T) {
	f, p, options := groupFixture(t)
	events.Subscribe(f.pipeline.Events, func(e events.Info) { t.Log(e.Message) })
	f.selector.participation.Mode = "open"
	p.audience = func(context.Context, discussion.AudienceRequest) (discussion.Audience, error) {
		return discussion.Audience{Kind: "current", Coverage: "none", Reason: "none"}, nil
	}
	p.next = func(_ context.Context, c discussion.Context) (discussion.Next, error) {
		if slices.Contains(c.Eligible, "p1") {
			return discussion.Next{Kind: "select", Member: "p1", Reason: "correction"}, nil
		}
		t.Fatalf("eligible=%v", c.Eligible)
		return discussion.Next{}, nil
	}
	p.check = func(_ context.Context, c discussion.Context, _ discussion.Persona) (discussion.Contribution, error) {
		return discussion.Contribution{Kind: "speak", Reason: "correction", Intent: "Add a concrete correction", References: []string{c.Delivered[0].ResponseID}}, nil
	}
	if err := f.pipeline.RunTurnTextMode(t.Context(), "Explain the tradeoff."); err != nil {
		t.Fatal(err)
	}
	if len(*options) != 2 || p.nextCalls != 1 || p.checks != 1 {
		t.Fatalf("replies=%d next=%d checks=%d", len(*options), p.nextCalls, p.checks)
	}
	if (*options)[0].Discussion != nil || (*options)[1].Discussion == nil {
		t.Fatal("open contribution did not use group admission")
	}
}

func TestGroupAudienceCannotApplyAfterParticipationChanges(t *testing.T) {
	f, policy, options := groupFixture(t)
	f.selector.opts.PersistParticipation = func(settings discussion.Settings) error { return f.store.SaveParticipation(f.session, settings) }
	original := policy.audience
	policy.audience = func(ctx context.Context, request discussion.AudienceRequest) (discussion.Audience, error) {
		if err := f.selector.SetParticipation(ctx, discussion.Settings{Version: discussion.Version, Mode: "invited", Members: []string{"samantha"}}); err != nil {
			t.Fatal(err)
		}
		return original(ctx, request)
	}
	err := f.pipeline.RunTurnTextMode(t.Context(), "Everyone, what do you think?")
	if !errors.Is(err, discussion.ErrStale) {
		t.Fatalf("stale audience error=%v", err)
	}
	if len(*options) != 0 || policy.checks != 0 {
		t.Fatal("stale audience admitted autonomous work")
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Participation == nil || !slices.Equal(saved.Participation.Members, []string{"samantha"}) {
		t.Fatalf("settings not persisted: %+v", saved.Participation)
	}
	if err := f.selector.RestoreParticipation(saved.Participation); err != nil {
		t.Fatal(err)
	}
	if f.selector.DiscussionSnapshot() != nil {
		t.Fatal("restoring settings resumed a discussion")
	}
}

func TestGroupMuteCancelsPrivateCheckBeforeAnyReply(t *testing.T) {
	f, policy, options := groupFixture(t)
	checking := make(chan struct{})
	policy.check = func(ctx context.Context, _ discussion.Context, _ discussion.Persona) (discussion.Contribution, error) {
		close(checking)
		<-ctx.Done()
		return discussion.Contribution{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second*5)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.pipeline.RunTurnTextMode(ctx, "Everyone, thoughts?") }()
	select {
	case <-checking:
	case <-ctx.Done():
		t.Fatal("private check did not start")
	}
	f.pipeline.SetOutputMuted(true)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("mute cancellation=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("mute did not cancel private inference")
	}
	if len(*options) != 0 || f.selector.DiscussionSnapshot() != nil {
		t.Fatal("mute left autonomous work alive")
	}
}

func TestGroupFirstOutputRechecksRosterAfterCommit(t *testing.T) {
	f, _, _ := groupFixture(t)
	admitted, err := f.selector.Route(t.Context(), "Everyone, thoughts?", false)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Discussion == nil || admitted.Discussion.BeforeOutput == nil {
		t.Fatal("no first-output guard")
	}
	if admitted.AgentMentionControl == nil || !admitted.AgentMentionControl("samantha") || admitted.AgentMentionControl("support") {
		t.Fatal("group admission did not carry the resolved speech controls")
	}
	if err := admitted.Discussion.BeforeOutput(t.Context()); err != nil {
		t.Fatalf("fresh output rejected: %v", err)
	}
	profile, err := f.catalog.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	profile.DisplayName = "Edited Samantha"
	writeTestProfile(t, f.catalog.Dir, *profile)
	if err := admitted.Discussion.BeforeOutput(t.Context()); err == nil {
		t.Fatal("roster changed after commit but stale output was accepted")
	}
}
