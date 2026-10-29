package personaruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type mentionReplyBrain struct {
	*selectorTestBrain
	reply   string
	options *[]brain.StreamOptions
}

func (b *mentionReplyBrain) ThinkStream(_ context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	*b.options = append(*b.options, opts)
	b.mu.Lock()
	if !opts.AgentContinuation {
		b.history = append(b.history, brain.Turn{Role: "user", Content: input})
	}
	b.history = append(b.history, brain.Turn{Role: "assistant", Content: b.reply, PersonaID: opts.Identity.PersonaID, PersonaDisplayName: opts.Identity.PersonaDisplayName, ResponseID: opts.Identity.ResponseID})
	b.working = cloneTestTurns(b.history)
	b.mu.Unlock()
	chunks := make(chan string, 1)
	chunks <- b.reply
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func TestExplicitAgentMentionStartsRecipientAndPersistsConversation(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("venessa-agent", "Venessa", "venessa"))
	defer f.close(t)
	var options []brain.StreamOptions
	response := "Sure thing, Lance. Let me connect with her now.\n@Venessa Hey Venessa! What would you like to cover?"
	f.pipeline.Brain = &mentionReplyBrain{selectorTestBrain: f.old, reply: response, options: &options}
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		reply := "Let's plan a festival."
		if cfg.ActivePersona == "obey" {
			reply = response
		}
		return &mentionReplyBrain{selectorTestBrain: newSelectorTestBrain(hash, nil), reply: reply, options: &options}, nil
	}
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		t.Error("explicit mention invoked semantic classifier")
		return brain.Addressee{}, errors.New("classifier offline")
	})
	f.pipeline.Events = events.NewBus()
	f.pipeline.AdmitInput = f.selector.Route
	f.pipeline.RouteHandoff = f.selector.RouteHandoff
	f.pipeline.OnTurn = func() {
		if err := f.store.Save(f.session, f.pipeline.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	if err := f.pipeline.RunTurnTextMode(t.Context(), "@obey Start the conversation."); err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[1].Identity.PersonaID != "venessa-agent" || !options[1].AgentContinuation {
		t.Fatalf("recipient did not respond as an agent continuation: %+v", options)
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SelectedPersonaID != "venessa-agent" {
		t.Fatalf("saved selection=%s", saved.SelectedPersonaID)
	}
	turns := saved.Turns
	if len(turns) < 3 || turns[len(turns)-2].Content != response || turns[len(turns)-1].PersonaID != "venessa-agent" || turns[len(turns)-1].Content != "Let's plan a festival." {
		t.Fatalf("persisted handoff lost response or invented a human turn: %+v", turns)
	}
}

func TestExplicitAgentHandoffGuards(t *testing.T) {
	for _, tc := range []struct {
		name, text                string
		visited                   []string
		cancel, inactive, wantErr bool
	}{
		{name: "self", text: "@Obey Hello"},
		{name: "repeat", text: "@Venessa Hello", visited: []string{"venessa"}},
		{name: "unknown", text: "@missing Hello", wantErr: true},
		{name: "inactive", text: "@Venessa Hello", inactive: true, wantErr: true},
		{name: "canceled", text: "@Venessa Hello", cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("venessa", "Venessa", "venessa"))
			defer f.close(t)
			if tc.inactive {
				profile := testProfile("venessa", "Venessa", "venessa")
				profile.Inactive = true
				writeTestProfile(t, f.catalog.Dir, profile)
				// Keep two eligible personas so the normal handoff path runs.
				writeTestProfile(t, f.catalog.Dir, testProfile("santa", "Santa", "santa"))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			_, accepted, err := f.selector.RouteHandoff(ctx, brain.Turn{Role: "assistant", PersonaID: "obey", Content: tc.text}, tc.visited)
			if accepted || (err != nil) != tc.wantErr || f.selector.Snapshot().PersonaID != "obey" {
				t.Fatalf("accepted=%v err=%v selection=%+v", accepted, err, f.selector.Snapshot())
			}
		})
	}
}

func TestExplicitHandoffRevalidatesAliasBeforeCommit(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("peer", "Venessa", "venessa"))
	defer f.close(t)
	proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "obey", Content: "@Venessa Hello"}, nil)
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	writeTestProfile(t, f.catalog.Dir, testProfile("peer", "Renamed", "venessa"))
	if _, err := proposal.Commit(t.Context()); err == nil || !strings.Contains(err.Error(), "changed target") {
		t.Fatalf("commit err=%v", err)
	}
	if f.selector.Snapshot().PersonaID != "obey" {
		t.Fatal("stale handoff changed selection")
	}
}
