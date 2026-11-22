package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// inviteRecorder plays a host whose first reply hands the floor to the first
// of several invited personas.
type inviteRecorder struct {
	mu         sync.Mutex
	spoke      []string
	classify   []string
	invites    []string
	inviters   []string
	failInvite string
	refuse     string
}

func (r *inviteRecorder) commit(persona string) func(context.Context) (ResponseAdmission, error) {
	return func(context.Context) (ResponseAdmission, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if persona == r.failInvite {
			return ResponseAdmission{}, errors.New(persona + " could not be loaded")
		}
		r.spoke = append(r.spoke, persona)
		return admission(persona), nil
	}
}

func (r *inviteRecorder) wire(p *Pipeline, first string, invited ...string) {
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("ada"), nil
	}
	p.RouteHandoff = func(_ context.Context, completed brain.Turn, _ []string) (HandoffProposal, bool, error) {
		r.mu.Lock()
		r.classify = append(r.classify, completed.PersonaID)
		calls := len(r.classify)
		r.mu.Unlock()
		if calls > 1 {
			return HandoffProposal{}, false, nil
		}
		proposal := HandoffProposal{PersonaID: first, Commit: r.commit(first)}
		for _, id := range invited {
			proposal.Invited = append(proposal.Invited, HandoffInvite{PersonaID: id, DisplayName: displayName(id)})
		}
		return proposal, true, nil
	}
	p.InviteHandoff = func(_ context.Context, completed brain.Turn, invite HandoffInvite, _ []string) (HandoffProposal, bool, error) {
		r.mu.Lock()
		r.invites = append(r.invites, invite.PersonaID)
		r.inviters = append(r.inviters, completed.PersonaID)
		refuse := r.refuse
		r.mu.Unlock()
		if refuse != "" {
			return HandoffProposal{}, false, &InviteRefusal{Reason: refuse}
		}
		return HandoffProposal{PersonaID: invite.PersonaID, Commit: r.commit(invite.PersonaID)}, true, nil
	}
}

func (r *inviteRecorder) snapshot() (spoke, classify, invites, inviters []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spoke), slices.Clone(r.classify), slices.Clone(r.invites), slices.Clone(r.inviters)
}

func displayName(id string) string {
	return map[string]string{"milo": "Milo", "vera": "Vera", "nick": "Nick", "nora": "Nora"}[id]
}

func infoMessages(bus *events.Bus) func() []string {
	var mu sync.Mutex
	var messages []string
	events.Subscribe(bus, func(e events.Info) {
		mu.Lock()
		messages = append(messages, e.Message)
		mu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(messages)
	}
}

func TestInvitedPersonasAnswerInOrderBeforeTheLastReplyIsClassified(t *testing.T) {
	for _, voice := range []bool{false, true} {
		name := "text"
		if voice {
			name = "voice"
		}
		t.Run(name, func(t *testing.T) {
			b := &continuationBrain{chunks: []string{"Answer."}}
			p := &Pipeline{Brain: b, Events: events.NewBus()}
			if voice {
				player := newFakePlayer(time.Millisecond)
				defer player.Close()
				p.STT, p.TTS, p.Player = &fakeSTT{text: "hello"}, &fakeTTS{}, player
			}
			r := &inviteRecorder{}
			r.wire(p, "milo", "vera")
			var err error
			if voice {
				_, err = p.RunTurn(t.Context())
			} else {
				err = p.RunTurnTextMode(t.Context(), "hello")
			}
			if err != nil {
				t.Fatal(err)
			}
			spoke, classify, invites, inviters := r.snapshot()
			if !slices.Equal(spoke, []string{"milo", "vera"}) {
				t.Fatalf("spoke after the first reply = %v, want milo then vera", spoke)
			}
			// Milo's reply is not classified while Vera waits; Vera's reply is.
			if !slices.Equal(classify, []string{"ada", "vera"}) {
				t.Fatalf("classified replies = %v, want ada and vera", classify)
			}
			if !slices.Equal(invites, []string{"vera"}) || !slices.Equal(inviters, []string{"milo"}) {
				t.Fatalf("invite hook got %v after %v", invites, inviters)
			}
			if inputs, options := b.calls(); len(inputs) != 3 || !options[1].AgentContinuation || !options[2].AgentContinuation {
				t.Fatalf("brain calls = %d, continuations %+v", len(inputs), options)
			}
		})
	}
}

func TestInviteQueueSkipsVisitedAndFailedInvitees(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	infos := infoMessages(p.Events)
	r := &inviteRecorder{failInvite: "vera"}
	r.wire(p, "milo", "ada", "vera", "nick")
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	spoke, _, invites, _ := r.snapshot()
	if !slices.Equal(spoke, []string{"milo", "nick"}) {
		t.Fatalf("spoke = %v, want milo then nick", spoke)
	}
	if !slices.Equal(invites, []string{"vera", "nick"}) {
		t.Fatalf("invite hook asked for %v; the persona who already spoke must not be asked", invites)
	}
	if got := infos(); len(got) != 1 || got[0] != "Agent handoff skipped: vera could not be loaded" {
		t.Fatalf("info = %q", got)
	}
}

func TestInviteLimitNamesInviteesLeftWithoutATurn(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	infos := infoMessages(p.Events)
	r := &inviteRecorder{}
	r.wire(p, "milo", "vera", "nick", "nora", "milo")
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	spoke, classify, _, _ := r.snapshot()
	if !slices.Equal(spoke, []string{"milo", "vera", "nick"}) || len(classify) != 1 {
		t.Fatalf("spoke = %v classified = %v", spoke, classify)
	}
	want := "Nora was invited but did not get a turn: a message allows 3 handoffs."
	if got := infos(); len(got) != 1 || got[0] != want {
		t.Fatalf("info = %q, want %q", got, want)
	}
}

func TestCapturedInputDropsTheInviteQueue(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	infos := infoMessages(p.Events)
	r := &inviteRecorder{}
	r.wire(p, "milo", "vera", "nick")
	routed := p.RouteHandoff
	p.RouteHandoff = func(ctx context.Context, completed brain.Turn, visited []string) (HandoffProposal, bool, error) {
		proposal, ok, err := routed(ctx, completed, visited)
		commit := proposal.Commit
		proposal.Commit = func(ctx context.Context) (ResponseAdmission, error) {
			// The user starts talking while Milo is answering.
			p.keepCapture.Store(true)
			return commit(ctx)
		}
		return proposal, ok, err
	}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	spoke, classify, invites, _ := r.snapshot()
	if !slices.Equal(spoke, []string{"milo"}) || len(invites) != 0 || len(classify) != 1 {
		t.Fatalf("spoke=%v invites=%v classified=%v", spoke, invites, classify)
	}
	if got := infos(); len(got) != 0 {
		t.Fatalf("a dropped invitation after user input must stay quiet: %q", got)
	}
}

func TestSpeechWhileAnInviteeIsDueEndsTheQueue(t *testing.T) {
	capture := newFakeCapture()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Capture: capture, BargeInVAD: &fakeVAD{}, Player: player}
	started := make(chan struct{})
	var asked []string
	p.InviteHandoff = func(ctx context.Context, _ brain.Turn, invite HandoffInvite, _ []string) (HandoffProposal, bool, error) {
		asked = append(asked, invite.PersonaID)
		close(started)
		<-ctx.Done()
		return HandoffProposal{PersonaID: invite.PersonaID, Commit: func(context.Context) (ResponseAdmission, error) {
			t.Error("interrupted invitation committed")
			return admission(invite.PersonaID), nil
		}}, true, nil
	}
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (HandoffProposal, bool, error) {
		t.Error("reply classified after the user spoke")
		return HandoffProposal{}, false, nil
	}
	type result struct {
		waiting  []HandoffInvite
		accepted bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, waiting, accepted, err := p.nextHandoff(t.Context(), &completionState{voice: true, ok: true}, []string{"ada", "milo"},
			[]HandoffInvite{{PersonaID: "vera"}, {PersonaID: "nick"}})
		done <- result{waiting, accepted, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting invitee was never proposed")
	}
	for range bargeInMinSpeechChunks + 1 {
		capture.Publish([]float32{.9, .9, .9})
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case got := <-done:
		if got.err != nil || got.accepted || len(got.waiting) != 0 {
			t.Fatalf("accepted=%v waiting=%v err=%v", got.accepted, got.waiting, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("speech did not end the invitation")
	}
	if !slices.Equal(asked, []string{"vera"}) || !p.HasCapturedInput() || capture.subCount() != 0 {
		t.Fatalf("asked=%v captured=%v watchers=%d", asked, p.HasCapturedInput(), capture.subCount())
	}
}

func TestDiscussionProposalIgnoresInvitees(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("ada"), nil
	}
	var routed int
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (HandoffProposal, bool, error) {
		routed++
		if routed > 1 {
			return HandoffProposal{}, false, nil
		}
		return HandoffProposal{DiscussionID: "talk", OpenAddition: true, PersonaID: "milo", Invited: []HandoffInvite{{PersonaID: "vera"}},
			Commit: func(context.Context) (ResponseAdmission, error) { return admission("milo"), nil }}, true, nil
	}
	p.InviteHandoff = func(context.Context, brain.Turn, HandoffInvite, []string) (HandoffProposal, bool, error) {
		t.Error("discussion proposal queued an invitee")
		return HandoffProposal{}, false, nil
	}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if inputs, _ := b.calls(); routed != 2 || len(inputs) != 2 {
		t.Fatalf("routed=%d brain calls=%d", routed, len(inputs))
	}
}

func TestRefusedInvitationEndsWithOneNotice(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	infos := infoMessages(p.Events)
	r := &inviteRecorder{refuse: "Milo cannot pass the conversation to another persona"}
	r.wire(p, "milo", "vera", "nick")
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	spoke, classify, invites, _ := r.snapshot()
	if !slices.Equal(spoke, []string{"milo"}) || !slices.Equal(invites, []string{"vera"}) || len(classify) != 1 {
		t.Fatalf("spoke=%v invites=%v classified=%v", spoke, invites, classify)
	}
	want := "Vera and Nick were invited but did not get a turn: Milo cannot pass the conversation to another persona."
	if got := infos(); len(got) != 1 || got[0] != want {
		t.Fatalf("info = %q, want %q", got, want)
	}
}

// failingReplyBrain answers normally except on the call numbered fail.
type failingReplyBrain struct {
	continuationBrain
	fail int
}

func (b *failingReplyBrain) ThinkStream(_ context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.inputs = append(b.inputs, input)
	b.options = append(b.options, opts)
	n := len(b.inputs)
	b.mu.Unlock()
	var result brain.StreamResult
	if n == b.fail {
		result = brain.StreamResult{Err: errors.New("provider failed"), Recovered: true}
	}
	chunks := make(chan string, 1)
	chunks <- "Answer."
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- result
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func TestFailedReplyEndsTheInvitationWithANotice(t *testing.T) {
	b := &failingReplyBrain{fail: 2}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	infos := infoMessages(p.Events)
	r := &inviteRecorder{}
	r.wire(p, "milo", "vera", "nick")
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	spoke, _, invites, _ := r.snapshot()
	if !slices.Equal(spoke, []string{"milo"}) || len(invites) != 0 {
		t.Fatalf("spoke=%v invites=%v", spoke, invites)
	}
	want := "Vera and Nick were invited but did not get a turn: the reply before theirs was interrupted or failed."
	if got := infos(); len(got) != 1 || got[0] != want {
		t.Fatalf("info = %q, want %q", got, want)
	}
}
