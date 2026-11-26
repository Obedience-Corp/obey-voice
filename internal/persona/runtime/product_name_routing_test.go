package personaruntime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestProductNameIsNotASummons(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		asked bool
	}{
		{input: "This is just a test of Obey voice.", want: "wren", asked: true},
		{input: "I like obey voice a lot", want: "wren", asked: true},
		{input: "Hey Obey voice, what time is it?", want: "wren", asked: true},
		{input: "Obey Voice, what time is it?", want: "wren", asked: true},
		{input: "Obey-voice, what time is it?", want: "wren", asked: true},
		{input: "Obey—voice, hi", want: "wren", asked: true},
		{input: "Obey - Voice is slow", want: "wren", asked: true},
		{input: "Is ObeyVoice running?", want: "wren"},
		{input: "Is ObeyVoice running, Wren?", want: "wren", asked: true},
		{input: "Obey what do you think", want: "obey", asked: true},
		{input: "what do you think, Obey?", want: "obey", asked: true},
		{input: "How do you like Obey Voice, Obey?", want: "obey", asked: true},
		{input: "Obey, how do you like Obey Voice?", want: "obey"},
		{input: "Obey, what time is it?", want: "obey"},
		{input: "Hey Obey,", want: "obey"},
		{input: "@obey what time is it", want: "obey"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("wren", "Wren", "wren"), testProfile("obey", "Obey", "obey"))
			defer f.close(t)
			watch := watchRouting(f.pipeline)
			var asked atomic.Int32
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				asked.Add(1)
				return brain.Addressee{Kind: "address", Name: "Obey"}, nil
			})
			got, err := f.selector.Route(t.Context(), tc.input, false)
			if err != nil || got.Envelope.PersonaID != tc.want {
				t.Fatalf("admission=%+v err=%v", got, err)
			}
			if (asked.Load() == 1) != tc.asked {
				t.Fatalf("classifier asked %d times, want asked=%v", asked.Load(), tc.asked)
			}
			if _, diagnostics := watch.snapshot(); tc.asked && (len(diagnostics) != 1 || diagnostics[0].Resolver != resolvedByClassifier) {
				t.Fatalf("diagnostics=%+v", diagnostics)
			}
		})
	}
}

func TestProductNameIsNotASummonsFromTheDecisionModel(t *testing.T) {
	for input, want := range map[string]string{
		"This is just a test of Obey Voice.": "wren",
		"what do you think, Obey?":           "obey",
	} {
		t.Run(input, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("wren", "Wren", "wren"), testProfile("obey", "Obey", "obey"))
			defer f.close(t)
			watch := watchRouting(f.pipeline)
			server := newDecisionServer(t, map[string]string{input: "Obey"})
			server.enable(t, f.pipeline)
			got, err := f.selector.Route(t.Context(), input, false)
			if err != nil || got.Envelope.PersonaID != want || server.calls.Load() != 1 {
				t.Fatalf("admission=%+v err=%v calls=%d", got, err, server.calls.Load())
			}
			if _, diagnostics := watch.snapshot(); len(diagnostics) != 1 || diagnostics[0].Resolver != resolvedByDecision {
				t.Fatalf("diagnostics=%+v", diagnostics)
			}
		})
	}
}

func TestProductNameIsNotASummonsInAHandoff(t *testing.T) {
	for text, want := range map[string]string{
		"This is just a test of Obey Voice.": "",
		"Obey, your thoughts?":               "obey",
	} {
		t.Run(text, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("wren", "Wren", "wren"), testProfile("obey", "Obey", "obey"))
			defer f.close(t)
			roster, participants, err := f.selector.conversationRoster()
			if err != nil {
				t.Fatal(err)
			}
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				return brain.Addressee{Kind: "address", Name: "Obey"}, nil
			})
			got, err := f.selector.resolveAddress(t.Context(), text, "agent", "Wren", roster, participants)
			if err != nil || got.PersonaID != want {
				t.Fatalf("address=%+v err=%v", got, err)
			}
		})
	}
}
