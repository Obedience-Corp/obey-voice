package personaruntime

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"testing"
)

func TestAcousticNameSupportsRoutingWithoutOverridingIntent(t *testing.T) {
	for _, tc := range []struct{ text, kind, target string }{
		{"Oh bay, explain that", "address", "Obey"},
		{"You must obey the rules", "none", ""},
		{"Tell me about Obey", "none", ""},
		{"He said Obey help me", "none", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("obey", "Obey", "samantha"))
			f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
				if r.Text != tc.text || r.AcousticName != "Obey" {
					t.Fatalf("request=%+v", r)
				}
				return brain.Addressee{Kind: tc.kind, Name: tc.target}, nil
			})
			got, err := f.selector.Route(admission.WithAcousticName(t.Context(), "Obey"), tc.text, false)
			want := "jarvis"
			if tc.kind == "address" {
				want = "obey"
			}
			if err != nil || got.Envelope.PersonaID != want || got.Request != tc.text {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestAcousticNameNotForwardedToAgentClassification(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("obey", "Obey", "samantha"))
	roster, participants, err := f.selector.conversationRoster()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
		called = true
		if r.AcousticName != "" {
			t.Fatal("user acoustic evidence reached agent handoff")
		}
		return brain.Addressee{Kind: "none"}, nil
	})
	_, err = f.selector.resolveAddress(admission.WithAcousticName(t.Context(), "Obey"), "Obey, your thoughts?", "agent", "Jarvis", roster, participants)
	if err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
