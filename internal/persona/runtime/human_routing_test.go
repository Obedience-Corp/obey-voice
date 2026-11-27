package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/summon"
)

type routingNames map[string]string

func (n routingNames) Display(id string) string {
	if name, ok := n[id]; ok {
		return name
	}
	return id
}

func TestHumanSpeakerNameNamesTheHuman(t *testing.T) {
	renames := routingNames{"speaker-1": "Ana"}
	for _, tc := range []struct {
		label, configured string
		names             brain.SpeakerNames
		want              string
	}{
		{"", "Lance", nil, "Lance"},
		{"", "", nil, "User"},
		{"unknown", "Lance", renames, "Lance"},
		{" Unknown ", "", nil, "User"},
		{"Maria", "Lance", renames, "Maria"},
		{"Maria", "", nil, "Maria"},
		{"speaker-1", "Lance", renames, "Ana"},
		{"speaker-2", "Lance", renames, "Unknown"},
	} {
		s := &Selector{opts: SelectionOptions{Base: &config.Config{UserDisplayName: tc.configured}, SpeakerNames: tc.names}}
		if got := s.humanSpeakerName(tc.label); got != tc.want {
			t.Errorf("label %q, configured %q: name = %q, want %q", tc.label, tc.configured, got, tc.want)
		}
	}
}

// The addressee classifier hears from the turn's own speaker, not the
// configured user, once that speaker is identified.
func TestAddresseeRequestNamesTheTurnSpeaker(t *testing.T) {
	for label, want := range map[string]string{"": "Lance", "unknown": "Lance", "Maria": "Maria"} {
		f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("santa", "Santa", "santa"))
		f.selector.opts.Base.UserDisplayName = "Lance"
		if _, err := f.selector.Admit(t.Context()); err != nil {
			t.Fatal(err)
		}
		var got string
		f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, req brain.AddressRequest) (brain.Addressee, error) {
			got = req.Speaker
			return brain.Addressee{Kind: "none"}, nil
		})
		if _, err := f.selector.Route(admission.WithSpeaker(t.Context(), label), "Let's talk about it. Obey, what do you think we should do?", false); err != nil {
			t.Fatal(err)
		}
		f.close(t)
		if got != want {
			t.Errorf("speaker %q: addressee request speaker = %q, want %q", label, got, want)
		}
	}
}

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
	input := "Let's talk about it a little bit. Obey, what do you think we should do?"
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
	for _, input := range []string{"@lance hello", "switch to Lance", "@ghost hello"} {
		_, err := f.selector.Route(t.Context(), input, false)
		var unknown *summon.UnknownTargetError
		if !errors.As(err, &unknown) {
			t.Fatalf("%q: expected unknown persona refusal, got %v", input, err)
		}
	}
}

func TestTopicCannotBecomeAnUnavailablePersona(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"))
	defer f.close(t)
	for _, tc := range []struct{ input, target string }{
		{"Do you know if there's a uh uncensored LFM3 model?", "LFM3"},
		{"Do you know if there's an uncensored LFM3 model?", "LFM3"},
		{"What can Cloudflare do?", "Cloudflare"},
		{"Is there an uncensored LFM3 model, Obey?", "LFM3"},
		{"Could Ghost answer?", "Ghost"},
	} {
		t.Run(tc.target+tc.input, func(t *testing.T) {
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				return brain.Addressee{Kind: "address", Name: tc.target}, nil
			})
			got, err := f.selector.Route(t.Context(), tc.input, false)
			if err != nil || got.Envelope.PersonaID != "obey" || got.Request != tc.input || got.LocalReply != "" {
				t.Fatalf("topic=%+v err=%v", got, err)
			}
		})
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
