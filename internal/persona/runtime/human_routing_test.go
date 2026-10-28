package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestHumanSpeakerCannotBecomePersonaOnFollowup(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	f.selector.opts.Base.UserDisplayName = "Lance"
	if _, err := f.selector.Admit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.pipeline.Brain.LoadHistory([]brain.Turn{
		{Role: "user", Content: "Can you help me create a new persona for Obey Voice?", Speaker: "unknown"},
		{Role: "assistant", Content: "Sure thing, Lance. What would you like the persona to be called?", PersonaDisplayName: "Obey"},
	})
	before := f.selector.Snapshot()
	f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, req brain.AddressRequest) (brain.Addressee, error) {
		if req.Speaker != "Lance" || req.Account != "user" || len(req.Conversation) != 2 || req.Conversation[0].Name != "Lance" {
			t.Errorf("human identity missing: %+v", req)
		}
		// Reproduce the model's observed confusion, even with corrected context.
		return brain.Addressee{Kind: "address", Name: " Lance ", Bare: true}, nil
	})
	input := "Let's talk about it a little bit. What do you think we should do?"
	got, err := f.selector.Route(t.Context(), input, false)
	if err != nil || got.Envelope.PersonaID != "obey" || got.Request != input || got.LocalReply != "" {
		t.Fatalf("followup=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(before, f.selector.Snapshot()) {
		t.Fatal("human classifier output changed selection")
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil || saved.SelectedPersonaID != "obey" {
		t.Fatalf("saved selection=%+v err=%v", saved, err)
	}
}

func TestHumanIdentityDoesNotHideExplicitUnknownPersona(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"))
	defer f.close(t)
	f.selector.opts.Base.UserDisplayName = "Lance"
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: "Ghost"}, nil
	})
	for _, input := range []string{"@lance hello", "switch to Lance", "Could Ghost answer?"} {
		_, err := f.selector.Route(t.Context(), input, false)
		var unknown *summon.UnknownTargetError
		if !errors.As(err, &unknown) {
			t.Fatalf("%q: expected unknown persona refusal, got %v", input, err)
		}
	}
}

func TestRealPersonaMayShareHumanDisplayName(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("lance-agent", "Lance", "samantha"))
	defer f.close(t)
	f.selector.opts.Base.UserDisplayName = "Lance"
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: "Lance"}, nil
	})
	got, err := f.selector.Route(t.Context(), "Could we hear from Lance on that?", false)
	if err != nil || got.Envelope.PersonaID != "lance-agent" {
		t.Fatalf("real persona=%+v err=%v", got, err)
	}
}
