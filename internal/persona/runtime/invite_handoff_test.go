package personaruntime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func inviteFixture(t *testing.T) *selectorTestFixture {
	t.Helper()
	return newSelectorTestFixture(t,
		testProfile("p-ada", "Ada", "ada"),
		testProfile("p-milo", "Milo", "milo"),
		testProfile("p-vera", "Vera", "vera"),
		testProfile("p-nick", "Nick", "nick"),
	)
}

func TestReplyMentioningSeveralPersonasInvitesThemInOrder(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		visited    []string
		target     string
		invited    []pipeline.HandoffInvite
	}{
		{name: "two", text: "@Milo @Vera, what do you two think?", target: "p-milo",
			invited: []pipeline.HandoffInvite{{PersonaID: "p-vera", DisplayName: "Vera"}}},
		{name: "speaker and visited dropped", text: "@Ada @Milo @Vera @Nick: thoughts?", visited: []string{"p-ada", "p-milo"}, target: "p-vera",
			invited: []pipeline.HandoffInvite{{PersonaID: "p-nick", DisplayName: "Nick"}}},
		{name: "repeats collapse", text: "@Vera, then @Milo, then @vera again.", target: "p-vera",
			invited: []pipeline.HandoffInvite{{PersonaID: "p-milo", DisplayName: "Milo"}}},
		{name: "nobody left", text: "@Ada and @Milo, thoughts?", visited: []string{"p-ada", "p-milo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := inviteFixture(t)
			defer f.close(t)
			proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "p-ada", Content: tc.text}, tc.visited)
			if err != nil || accepted != (tc.target != "") || proposal.PersonaID != tc.target || !slices.Equal(proposal.Invited, tc.invited) {
				t.Fatalf("accepted=%v err=%v proposal=%s then %+v", accepted, err, proposal.PersonaID, proposal.Invited)
			}
			if f.selector.Snapshot().PersonaID != "p-ada" {
				t.Fatal("proposal changed selection before commit")
			}
		})
	}
}

func TestInvitationRevalidatesEveryNamedPersonaBeforeCommit(t *testing.T) {
	f := inviteFixture(t)
	defer f.close(t)
	proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "p-ada", Content: "@Milo @Vera, thoughts?"}, nil)
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	// Only the second invitee changes; the first still resolves.
	writeTestProfile(t, f.catalog.Dir, testProfile("p-vera", "Renamed", "vera"))
	writeTestProfile(t, f.catalog.Dir, testProfile("p-other", "Vera", "other"))
	if _, err := proposal.Commit(t.Context()); err == nil || !strings.Contains(err.Error(), "changed target") {
		t.Fatalf("commit err=%v", err)
	}
	if f.selector.Snapshot().PersonaID != "p-ada" {
		t.Fatal("stale invitation changed selection")
	}
}

func TestInviteHandoffBelongsToThePersonaWhoJustAnswered(t *testing.T) {
	f := inviteFixture(t)
	defer f.close(t)
	invite := pipeline.HandoffInvite{PersonaID: "p-vera", DisplayName: "Vera"}
	if _, accepted, err := f.selector.InviteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "p-milo", Content: "Fine."}, invite, nil); accepted || err != nil {
		t.Fatalf("reply from an unselected persona: accepted=%v err=%v", accepted, err)
	}
	if _, accepted, _ := f.selector.InviteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "p-ada", Content: "Fine."}, invite, []string{"p-vera"}); accepted {
		t.Fatal("visited invitee proposed")
	}
	proposal, accepted, err := f.selector.InviteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "p-ada", Content: "Fine."}, invite, nil)
	if err != nil || !accepted || proposal.PersonaID != "p-vera" {
		t.Fatalf("accepted=%v err=%v proposal=%+v", accepted, err, proposal)
	}
	if _, err := f.selector.selectAtBoundary(t.Context(), "p-nick", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := proposal.Commit(t.Context()); err == nil || !strings.Contains(err.Error(), "changed persona selection") {
		t.Fatalf("commit after the selection moved: err=%v", err)
	}
	if f.selector.Snapshot().PersonaID != "p-nick" {
		t.Fatal("stale invitation changed selection")
	}
}

func TestInvitedPersonasAnswerAReplyInTurn(t *testing.T) {
	f := inviteFixture(t)
	defer f.close(t)
	var options []brain.StreamOptions
	invitation := "Good question. @Milo @Vera, what do you two think?"
	replies := map[string]string{"p-ada": invitation, "p-milo": "Milo here: start small.", "p-vera": "Vera here: agreed."}
	f.pipeline.Brain = &mentionReplyBrain{selectorTestBrain: f.old, reply: invitation, options: &options}
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return &mentionReplyBrain{selectorTestBrain: newSelectorTestBrain(hash, nil), reply: replies[cfg.ActivePersona], options: &options}, nil
	}
	var classified []string
	f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
		classified = append(classified, r.Text)
		return brain.Addressee{Kind: "none"}, nil
	})
	f.pipeline.Events = events.NewBus()
	var infos []string
	events.Subscribe(f.pipeline.Events, func(e events.Info) { infos = append(infos, e.Message) })
	f.pipeline.AdmitInput = f.selector.Route
	f.pipeline.RouteHandoff = f.selector.RouteHandoff
	f.pipeline.InviteHandoff = f.selector.InviteHandoff
	if err := f.pipeline.RunTurnTextMode(t.Context(), "@p-ada What should we build first?"); err != nil {
		t.Fatal(err)
	}
	var spoke []string
	for i, opts := range options {
		spoke = append(spoke, opts.Identity.PersonaID)
		if opts.AgentContinuation != (i > 0) {
			t.Fatalf("request %d continuation=%v", i, opts.AgentContinuation)
		}
	}
	if !slices.Equal(spoke, []string{"p-ada", "p-milo", "p-vera"}) {
		t.Fatalf("answered in order %v, want Ada, Milo, Vera", spoke)
	}
	if f.selector.Snapshot().PersonaID != "p-vera" {
		t.Fatalf("selection = %s", f.selector.Snapshot().PersonaID)
	}
	var said []string
	for _, turn := range f.selector.Transcript() {
		if turn.Role == "assistant" {
			said = append(said, turn.PersonaID+": "+turn.Content)
		}
	}
	if !slices.Equal(said, []string{"p-ada: " + invitation, "p-milo: Milo here: start small.", "p-vera: Vera here: agreed."}) {
		t.Fatalf("transcript replies = %q", said)
	}
	// Milo's reply names a persona but is not classified while Vera waits.
	if !slices.Equal(classified, []string{"Vera here: agreed."}) {
		t.Fatalf("classified %q", classified)
	}
	if len(infos) != 0 {
		t.Fatalf("unexpected notices %q", infos)
	}
}
