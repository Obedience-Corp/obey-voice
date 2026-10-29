package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestPlanningExactControlSurvivesClassifierOutage(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		cancel bool
	}{
		{name: "bare_name", input: "Santa"},
		{name: "supported_greeting", input: "Hey Santa"},
		{name: "explicit_switch", input: "switch to Santa"},
		{name: "parent_canceled_control", input: "Santa", cancel: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "samantha"))
			defer f.close(t)
			calls := 0
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				calls++
				return brain.Addressee{}, errors.New("classifier offline")
			})
			ctx := t.Context()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := f.selector.Route(ctx, tc.input, false)
			if tc.cancel {
				if !errors.Is(err, context.Canceled) || calls != 0 || got.Envelope.PersonaID != "" || got.LocalReply != "" {
					t.Fatalf("control=%q persona=%q local=%q classifier_calls=%d err=%v", tc.input, got.Envelope.PersonaID, got.LocalReply, calls, err)
				}
				return
			}
			if err != nil || got.Envelope.PersonaID != "santa" || got.LocalReply != "Santa here." || calls != 0 {
				t.Fatalf("control=%q persona=%q local=%q classifier_calls=%d err=%v", tc.input, got.Envelope.PersonaID, got.LocalReply, calls, err)
			}
		})
	}
}

func TestExactControlAliasWinsOverGreetingAndSwitchGrammar(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helper := testProfile("helper", "Helper", "helper")
	helper.Aliases = []string{"Hey Santa", "switch to Santa"}
	f := newSelectorTestFixture(t, obey, santa, helper)
	defer f.close(t)
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{}, errors.New("classifier should not run for exact controls")
	})
	for _, input := range []string{"Hey Santa", "switch to Santa"} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != "helper" || got.LocalReply != "Helper here." || calls != 0 {
			t.Fatalf("input=%q admission=%+v calls=%d err=%v", input, got, calls, err)
		}
	}
}

func TestExactControlPunctuatedAliasPreventsWrongPersonaSelection(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helper := testProfile("helper", "Helper", "helper")
	helper.Aliases = []string{"Santa!", "Hey Santa!", "switch to Santa!"}
	f := newSelectorTestFixture(t, obey, santa, helper)
	defer f.close(t)
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{}, errors.New("classifier should not run for exact controls")
	})
	for _, input := range []string{"Santa!", "Hey Santa!", "switch to Santa!", "Santa!!", "Hey Santa!!", "switch to Santa!!"} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != "helper" || got.LocalReply != "Helper here." || calls != 0 {
			t.Fatalf("input=%q admission=%+v calls=%d err=%v", input, got, calls, err)
		}
	}
}

func TestExactControlCoordinatorPunctuatedAliasOnlySelectsHelper(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helper := testProfile("helper", "Helper", "helper")
	helper.Aliases = []string{"Santa!"}
	f := newSelectorTestFixture(t, obey, santa, helper)
	defer f.close(t)
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{}, errors.New("classifier should not run for exact controls")
	})
	for _, input := range []string{"Santa!", "Hey Santa!", "switch to Santa!", "Santa!!", "Santa!?", "Santa!\u2026", "Hey Santa!!", "switch to Santa!!"} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != "helper" || got.LocalReply != "Helper here." || got.Request != "" || calls != 0 {
			t.Fatalf("input=%q admission=%+v calls=%d err=%v", input, got, calls, err)
		}
	}
}

func TestExactControlFullInputAmbiguityRefusesWithoutGrammarFallback(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helperA := testProfile("helper-a", "Helper A", "helper-a")
	helperA.Aliases = []string{"Hey Santa"}
	helperB := testProfile("helper-b", "Helper B", "helper-b")
	helperB.Aliases = []string{"Hey Santa"}
	f := newSelectorTestFixture(t, obey, santa, helperA, helperB)
	defer f.close(t)
	before := f.selector.Snapshot()
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	_, err := f.selector.Route(t.Context(), "Hey Santa", false)
	var ambiguous *summon.AmbiguousTargetError
	if !errors.As(err, &ambiguous) || calls != 0 || f.selector.Snapshot() != before {
		t.Fatalf("err=%v calls=%d snapshot=%+v before=%+v", err, calls, f.selector.Snapshot(), before)
	}
}

func TestExactControlCoordinatorPunctuatedAliasOnlyAmbiguityRefusesWithoutSideEffects(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helperA := testProfile("helper-a", "Helper A", "helper-a")
	helperA.Aliases = []string{"Santa!"}
	helperB := testProfile("helper-b", "Helper B", "helper-b")
	helperB.Aliases = []string{"Santa!"}
	f := newSelectorTestFixture(t, obey, santa, helperA, helperB)
	defer f.close(t)
	before := f.selector.Snapshot()
	savedBefore, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	for _, input := range []string{"Santa!", "Hey Santa!", "switch to Santa!", "Santa!!", "Santa!?", "Santa!\u2026", "Hey Santa!!", "switch to Santa!!"} {
		admitted, err := f.selector.Route(t.Context(), input, false)
		var ambiguous *summon.AmbiguousTargetError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("input=%q err=%v", input, err)
		}
		if admitted.Request != input || admitted.Envelope.PersonaID != "" || admitted.LocalReply != "" {
			t.Fatalf("input=%q admitted=%+v", input, admitted)
		}
		if calls != 0 || f.selector.Snapshot() != before || f.pipeline.Brain != f.old {
			t.Fatalf("input=%q calls=%d snapshot=%+v before=%+v", input, calls, f.selector.Snapshot(), before)
		}
		savedAfter, loadErr := f.store.Load(f.session.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if savedAfter.SelectedPersonaID != savedBefore.SelectedPersonaID || savedAfter.SelectedPersonaPromptHash != savedBefore.SelectedPersonaPromptHash || !reflect.DeepEqual(savedAfter.Turns, savedBefore.Turns) {
			t.Fatalf("input=%q persistence changed: before=%+v after=%+v", input, savedBefore, savedAfter)
		}
	}
}

func TestExactControlPunctuatedAliasAmbiguityRefusesWithoutSideEffects(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	helperA := testProfile("helper-a", "Helper A", "helper-a")
	helperA.Aliases = []string{"Santa!", "Hey Santa!", "switch to Santa!"}
	helperB := testProfile("helper-b", "Helper B", "helper-b")
	helperB.Aliases = []string{"Santa!", "Hey Santa!", "switch to Santa!"}
	f := newSelectorTestFixture(t, obey, santa, helperA, helperB)
	defer f.close(t)
	before := f.selector.Snapshot()
	savedBefore, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	for _, input := range []string{"Santa!", "Hey Santa!", "switch to Santa!", "Santa!!", "Hey Santa!!", "switch to Santa!!"} {
		_, err := f.selector.Route(t.Context(), input, false)
		var ambiguous *summon.AmbiguousTargetError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("input=%q err=%v", input, err)
		}
		if calls != 0 || f.selector.Snapshot() != before || f.pipeline.Brain != f.old {
			t.Fatalf("input=%q calls=%d snapshot=%+v before=%+v", input, calls, f.selector.Snapshot(), before)
		}
		savedAfter, loadErr := f.store.Load(f.session.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if savedAfter.SelectedPersonaID != savedBefore.SelectedPersonaID || savedAfter.SelectedPersonaPromptHash != savedBefore.SelectedPersonaPromptHash || !reflect.DeepEqual(savedAfter.Turns, savedBefore.Turns) {
			t.Fatalf("input=%q persistence changed: before=%+v after=%+v", input, savedBefore, savedAfter)
		}
	}
}

func TestExactControlNegativeCasesStillInvokeResolver(t *testing.T) {
	for _, input := range []string{`"Santa"`, "switch to Santa and tell me a story"} {
		t.Run(input, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			defer f.close(t)
			calls := 0
			f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, request brain.AddressRequest) (brain.Addressee, error) {
				calls++
				if request.Text != input {
					t.Fatalf("request text=%q", request.Text)
				}
				return brain.Addressee{Kind: "none"}, nil
			})
			if _, err := f.selector.Route(t.Context(), input, false); err != nil {
				t.Fatalf("route err=%v", err)
			}
			if calls != 1 {
				t.Fatalf("resolver calls=%d, want 1", calls)
			}
		})
	}
}

func TestExactControlInvalidSwitchRefusesWithoutResolver(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	for _, input := range []string{"switch to Unknown", "switch to Unknown!"} {
		before := f.selector.Snapshot()
		calls := 0
		f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
			calls++
			return brain.Addressee{Kind: "none"}, nil
		})
		_, err := f.selector.Route(t.Context(), input, false)
		var unknown *summon.UnknownTargetError
		if !errors.As(err, &unknown) || calls != 0 || f.selector.Snapshot() != before {
			t.Fatalf("input=%q err=%v calls=%d snapshot=%+v before=%+v", input, err, calls, f.selector.Snapshot(), before)
		}
	}
}

func TestExactControlBypassForBackgroundTurns(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	before := f.selector.Snapshot()
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{Kind: "address", Name: "Santa", Bare: true}, nil
	})
	got, err := f.selector.Route(t.Context(), "Santa", true)
	if err != nil {
		t.Fatal(err)
	}
	after := f.selector.Snapshot()
	if calls != 0 || got.LocalReply != "" || got.Envelope.PersonaID != before.PersonaID || after.PersonaID != before.PersonaID || got.Request != "Santa" {
		t.Fatalf("background route=%+v calls=%d snapshot=%+v before=%+v", got, calls, after, before)
	}
}

func TestExactControlSupportsUnicodeFullNameWithoutResolver(t *testing.T) {
	base := testProfile("jarvis", "Jarvis", "jarvis")
	unicode := testProfile("santa-north", "Sánta North", "santa-north")
	f := newSelectorTestFixture(t, base, unicode)
	defer f.close(t)
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{}, errors.New("classifier should not run")
	})
	got, err := f.selector.Route(t.Context(), "Sánta North.", false)
	if err != nil || got.Envelope.PersonaID != "santa-north" || got.LocalReply != "Sánta North here." || calls != 0 {
		t.Fatalf("unicode control admission=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestExactControlStaleAliasChangeIsRefusedBeforeCommit(t *testing.T) {
	obey := testProfile("obey", "Obey", "obey")
	santa := testProfile("santa", "Santa", "santa")
	santa.Aliases = []string{"Holiday"}
	helper := testProfile("helper", "Helper", "helper")
	f := newSelectorTestFixture(t, obey, santa, helper)
	defer f.close(t)
	initial := f.selector.Snapshot()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		candidate := newSelectorTestBrain(hash, nil)
		f.factoryMu.Lock()
		f.factories = append(f.factories, candidate)
		f.factoryMu.Unlock()
		return candidate, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.selector.Route(context.Background(), "Holiday", false)
		result <- err
	}()
	<-started
	santa.Aliases = nil
	helper.Aliases = []string{"Holiday"}
	writeTestProfile(t, f.catalog.Dir, santa)
	writeTestProfile(t, f.catalog.Dir, helper)
	close(release)
	err := <-result
	if err == nil || !strings.Contains(err.Error(), "changed target") {
		t.Fatalf("stale alias remap err=%v", err)
	}
	if f.selector.Snapshot() != initial || f.pipeline.Brain != f.old {
		t.Fatal("stale alias remap changed active persona")
	}
	saved, loadErr := f.store.Load(f.session.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.SelectedPersonaID != initial.PersonaID {
		t.Fatalf("persisted persona=%s", saved.SelectedPersonaID)
	}
}

func TestExactControlCancellationDuringPreparationKeepsSelection(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	initial := f.selector.Snapshot()
	started := make(chan struct{})
	release := make(chan struct{})
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		close(started)
		<-release
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return newSelectorTestBrain(hash, nil), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := f.selector.Route(ctx, "Santa", false)
		result <- err
	}()
	<-started
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled route err=%v", err)
	}
	if f.selector.Snapshot() != initial || f.pipeline.Brain != f.old {
		t.Fatal("canceled exact control changed active persona")
	}
}

func TestExactControlPersistenceFailureLeavesOldIdentity(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	initial := f.selector.Snapshot()
	f.selector.opts.Persist = func([]brain.Turn, events.PersonaSelection) error { return errors.New("disk full") }
	got, err := f.selector.Route(t.Context(), "Santa", false)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("route=%+v err=%v", got, err)
	}
	if f.selector.Snapshot() != initial || f.pipeline.Brain != f.old {
		t.Fatal("persistence failure changed active persona")
	}
	saved, loadErr := f.store.Load(f.session.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.SelectedPersonaID != initial.PersonaID {
		t.Fatalf("persisted persona=%s", saved.SelectedPersonaID)
	}
}
