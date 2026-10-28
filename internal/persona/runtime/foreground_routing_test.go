package personaruntime

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestNamelessRepliesNeverReclassifyPersona(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("joker", "Joker", "joker"), testProfile("venessa", "Venessa", "venessa"))
	defer f.close(t)
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		t.Fatal("no candidate agent to classify")
		return brain.Addressee{}, nil
	})
	for _, input := range []string{"yes", "no", "do it", "don't do it", "Okay, tell me the weather.", "Let's let the uh chaos happen.", "Is there an uncensored LFM3 model?"} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != "joker" || got.Request != input || got.LocalReply != "" {
			t.Fatalf("%q: %+v %v", input, got, err)
		}
	}
}

func TestLeadingListenerOwnsPeerRequest(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("joker", "Joker", "joker"), testProfile("venessa", "Venessa", "venessa"))
	defer f.close(t)
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		t.Fatal("explicit listener reached classifier")
		return brain.Addressee{}, nil
	})
	if _, err := f.selector.Select(t.Context(), "venessa", "fixture"); err != nil {
		t.Fatal(err)
	}
	input := "Joker, ask Venessa what she thinks and let her answer."
	got, err := f.selector.Route(t.Context(), input, false)
	if err != nil || got.Envelope.PersonaID != "joker" || got.Request != input || got.LocalReply != "" {
		t.Fatalf("request=%+v err=%v", got, err)
	}
}
