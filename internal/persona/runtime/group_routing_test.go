package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

type addressResolverFunc func(context.Context, brain.AddressRequest) (brain.Addressee, error)

func (f addressResolverFunc) ResolveAddressee(ctx context.Context, r brain.AddressRequest) (brain.Addressee, error) {
	return f(ctx, r)
}

func TestSemanticAddressUsesWholeUtteranceAndPreservesOriginal(t *testing.T) {
	for _, input := range []string{"Santa what do you think", "What do you think Santa", "Could we hear from Santa on that?", "Santa, a penny for your thoughts"} {
		t.Run(input, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "samantha"))
			f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
				if r.Text != input || r.Account != "user" {
					t.Fatalf("request=%+v", r)
				}
				for _, p := range r.Participants {
					if p.Name == "" {
						t.Fatal("missing public persona name")
					}
				}
				return brain.Addressee{Kind: "address", Name: "Santa"}, nil
			})
			got, err := f.selector.Route(t.Context(), input, false)
			if err != nil || got.Envelope.PersonaID != "santa" || got.Request != input {
				t.Fatalf("admission=%+v err=%v", got, err)
			}
		})
	}
}

func TestSemanticRoutingRefusesUncertainAndUnavailableTargets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision brain.Addressee
		err      error
		wantErr  bool
	}{
		{name: "mention in passing", decision: brain.Addressee{Kind: "none"}},
		{name: "unknown", decision: brain.Addressee{Kind: "address", Name: "Charles"}, wantErr: true},
		{name: "ambiguous", decision: brain.Addressee{Kind: "ambiguous", Name: "Santa"}, wantErr: true},
		{name: "invalid", decision: brain.Addressee{Kind: "oops"}, wantErr: true},
		{name: "failed", err: errors.New("classifier offline"), wantErr: true},
		{name: "cancelled", err: context.Canceled, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "samantha"))
			if _, err := f.selector.Admit(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := f.selector.Snapshot()
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) { return tc.decision, tc.err })
			_, err := f.selector.Route(t.Context(), "What do you think about Santa", false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if !reflect.DeepEqual(before, f.selector.Snapshot()) {
				t.Fatal("uncertain decision changed persona")
			}
		})
	}
}

func TestSemanticCollisionStillFailsClosed(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("santa1", "Santa North", "samantha"), testProfile("santa2", "Santa South", "samantha"))
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	_, err := f.selector.Route(t.Context(), "What do you think Santa", false)
	var ambiguous *summon.AmbiguousTargetError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentHandoffPreservesTranscriptAndRefusesRepeat(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "samantha"))
	initial := f.selector.Snapshot()
	response := brain.Turn{Role: "assistant", Content: "What do you think, Santa?", PersonaID: initial.PersonaID, PersonaDisplayName: initial.DisplayName}
	before := f.pipeline.Brain.History()
	f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
		if r.Account != "agent" || r.Text != response.Content {
			t.Fatalf("request=%+v", r)
		}
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	_, ok, err := f.selector.RouteHandoff(t.Context(), response, []string{initial.PersonaID, "santa"})
	if err != nil || ok || f.selector.Snapshot().PersonaID != initial.PersonaID {
		t.Fatalf("repeat ok=%v err=%v", ok, err)
	}
	admitted, ok, err := f.selector.RouteHandoff(t.Context(), response, []string{initial.PersonaID})
	if err != nil || !ok || admitted.Envelope.PersonaID != "santa" || admitted.Request != "" {
		t.Fatalf("handoff=%+v ok=%v err=%v", admitted, ok, err)
	}
	if !reflect.DeepEqual(before, f.pipeline.Brain.History()) {
		t.Fatal("handoff invented a transcript turn")
	}
}

func TestPersonaRoundTripTransfersCompactedWorkingView(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)

	canonical := []brain.Turn{
		{Role: "user", Content: "the original request"},
		{Role: "assistant", Content: "the old answer", PersonaID: "obey", PersonaDisplayName: "Obey"},
		{Role: "user", Content: "the latest request"},
		{Role: "assistant", Content: "the latest answer", PersonaID: "obey", PersonaDisplayName: "Obey"},
	}
	working := []brain.Turn{
		{Role: "assistant", Content: "Earlier context summary."},
		{Role: "user", Content: "the latest request"},
		{Role: "assistant", Content: "the latest answer", PersonaID: "obey", PersonaDisplayName: "Obey"},
	}
	f.old.mu.Lock()
	f.old.history, f.old.working = cloneTestTurns(canonical), cloneTestTurns(working)
	f.old.mu.Unlock()

	if _, err := f.selector.Select(t.Context(), "santa", "name"); err != nil {
		t.Fatal(err)
	}
	santa := f.factories[len(f.factories)-1]
	santa.mu.Lock()
	santa.history = append(santa.history, brain.Turn{Role: "user", Content: "new message"})
	santa.working = append(santa.working, brain.Turn{Role: "user", Content: "new message"})
	santa.mu.Unlock()

	if _, err := f.selector.Select(t.Context(), "obey", "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "santa", "name"); err != nil {
		t.Fatal(err)
	}
	last := f.factories[len(f.factories)-1]
	last.mu.Lock()
	defer last.mu.Unlock()
	if len(last.imports) == 0 || len(last.imports[len(last.imports)-1]) != 4 {
		t.Fatalf("transferred working view = %#v, want compact seed and new message", last.imports)
	}
	if got := last.history; len(got) != len(canonical)+1 || got[len(got)-1].Content != "new message" {
		t.Fatalf("canonical history after round trip = %#v", got)
	}
}
