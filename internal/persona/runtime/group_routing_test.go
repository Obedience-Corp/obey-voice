package personaruntime

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestSemanticRoutingClassifiesAdmissionFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     string
		configure func(*selectorTestFixture)
		ctx       func(context.Context) (context.Context, context.CancelFunc)
		want      admission.FailureCategory
	}{
		{
			name:  "classifier unavailable",
			input: "What do you think Santa",
			configure: func(f *selectorTestFixture) {
				f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
					return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierUnavailable, errors.New("transport unavailable"))
				})
			},
			ctx:  func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, nil },
			want: admission.FailureClassifierUnavailable,
		},
		{
			name:  "classifier local timeout",
			input: "What do you think Santa",
			configure: func(f *selectorTestFixture) {
				f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
					return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, context.DeadlineExceeded)
				})
			},
			ctx:  func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, nil },
			want: admission.FailureClassifierLocalTimeout,
		},
		{
			name:  "invalid classifier output",
			input: "What do you think Santa",
			configure: func(f *selectorTestFixture) {
				f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
					return brain.Addressee{Kind: "invalid"}, nil
				})
			},
			ctx:  func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, nil },
			want: admission.FailureClassifierInvalidOutput,
		},
		{
			name:  "identity refusal on unknown target",
			input: "What do you think Santa",
			configure: func(f *selectorTestFixture) {
				f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
					return brain.Addressee{Kind: "address", Name: "Ghost"}, nil
				})
			},
			ctx:  func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, nil },
			want: admission.FailureIdentityRefusal,
		},
		{
			name:      "parent canceled",
			input:     "What do you think Santa",
			configure: func(*selectorTestFixture) {},
			ctx: func(ctx context.Context) (context.Context, context.CancelFunc) {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				return cctx, nil
			},
			want: admission.FailureParentCancellation,
		},
		{
			name:      "parent deadline",
			input:     "What do you think Santa",
			configure: func(*selectorTestFixture) {},
			ctx: func(ctx context.Context) (context.Context, context.CancelFunc) {
				dctx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				return dctx, cancel
			},
			want: admission.FailureParentCancellation,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			tc.configure(f)
			ctx, cancel := tc.ctx(t.Context())
			if cancel != nil {
				defer cancel()
			}
			_, err := f.selector.Route(ctx, tc.input, false)
			var failure *admission.Failure
			if !errors.As(err, &failure) || failure.Category != tc.want {
				t.Fatalf("err=%v failure=%+v want=%s", err, failure, tc.want)
			}
		})
	}
}

func TestSemanticRoutingStaleCatalogClassifiedAsIdentityRefusal(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	if err := os.RemoveAll(f.catalog.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.catalog.Dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := f.selector.Route(t.Context(), "What do you think Santa", false)
	var failure *admission.Failure
	if !errors.As(err, &failure) || failure.Category != admission.FailureIdentityRefusal {
		t.Fatalf("err=%v failure=%+v", err, failure)
	}
}

func TestSemanticRoutingSelectionFailureClassifiedAsIdentityRefusal(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	f.selector.opts.NewBrain = func(context.Context, *config.Config) (brain.Provider, error) {
		return nil, errors.New("brain unavailable")
	}
	_, err := f.selector.Route(t.Context(), "What do you think Santa", false)
	var failure *admission.Failure
	if !errors.As(err, &failure) || failure.Category != admission.FailureIdentityRefusal {
		t.Fatalf("err=%v failure=%+v", err, failure)
	}
}

func TestRouteHandoffCatalogFailureClassifiedAsIdentityRefusal(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	if err := os.RemoveAll(f.catalog.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.catalog.Dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	response := brain.Turn{
		Role:               "assistant",
		Content:            "What do you think, Santa?",
		PersonaID:          f.selector.Snapshot().PersonaID,
		PersonaDisplayName: f.selector.Snapshot().DisplayName,
	}
	_, accepted, err := f.selector.RouteHandoff(t.Context(), response, nil)
	var failure *admission.Failure
	if accepted || !errors.As(err, &failure) || failure.Category != admission.FailureIdentityRefusal {
		t.Fatalf("accepted=%v err=%v failure=%+v", accepted, err, failure)
	}
}

func TestManualRetryAfterClassifierFailureUsesCurrentRoster(t *testing.T) {
	jarvis := testProfile("jarvis", "Jarvis", "jarvis")
	santa := testProfile("santa", "Santa", "santa")
	f := newSelectorTestFixture(t, jarvis, santa)
	defer f.close(t)

	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		switch calls {
		case 1:
			return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierUnavailable, errors.New("classifier offline"))
		case 2:
			return brain.Addressee{Kind: "address", Name: "Saint Nick"}, nil
		default:
			return brain.Addressee{Kind: "none"}, nil
		}
	})

	_, err := f.selector.Route(t.Context(), "What do you think Santa?", false)
	var failure *admission.Failure
	if !errors.As(err, &failure) || failure.Category != admission.FailureClassifierUnavailable {
		t.Fatalf("first route err=%v failure=%+v", err, failure)
	}

	// Simulate a roster change before the user's manual retry. The second route
	// must read the current roster, not reuse stale failure-era state.
	santa.DisplayName = "Saint Nick"
	writeTestProfile(t, f.catalog.Dir, santa)

	admitted, err := f.selector.Route(t.Context(), "What do you think Saint Nick?", false)
	if err != nil {
		t.Fatalf("retry route err=%v", err)
	}
	if admitted.Envelope.PersonaID != "santa" || admitted.Request != "What do you think Saint Nick?" {
		t.Fatalf("retry admission=%+v", admitted)
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
	proposal, ok, err := f.selector.RouteHandoff(t.Context(), response, []string{initial.PersonaID})
	if err != nil || !ok {
		t.Fatalf("proposal=%+v ok=%v err=%v", proposal, ok, err)
	}
	if f.selector.Snapshot() != initial {
		t.Fatal("classification changed selection")
	}
	admitted, err := proposal.Commit(t.Context())
	if err != nil || admitted.Envelope.PersonaID != "santa" || admitted.Request != "" {
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

func TestHandoffProposalFailuresLeaveSelectionAndDiskUnchanged(t *testing.T) {
	for _, failure := range []string{"canceled", "roster", "prepare", "persist"} {
		t.Run(failure, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			defer f.close(t)
			initial := f.selector.Snapshot()
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				return brain.Addressee{Kind: "address", Name: "Santa"}, nil
			})
			proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: initial.PersonaID, Content: "Santa, your thoughts?"}, nil)
			if err != nil || !accepted {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
			if len(f.factories) != 0 || f.selector.Snapshot() != initial {
				t.Fatal("classification prepared or selected persona")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "canceled":
				cancel()
			case "roster":
				if err := os.RemoveAll(f.catalog.Dir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.catalog.Dir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "prepare":
				f.selector.opts.NewBrain = func(context.Context, *config.Config) (brain.Provider, error) { return nil, errors.New("unavailable") }
			case "persist":
				f.selector.opts.Persist = func([]brain.Turn, events.PersonaSelection) error { return errors.New("disk full") }
			}
			if _, err := proposal.Commit(ctx); err == nil {
				t.Fatal("expected failed admission")
			}
			if f.selector.Snapshot() != initial || f.pipeline.Brain != f.old {
				t.Fatal("failed handoff changed active persona")
			}
			saved, err := f.store.Load(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.SelectedPersonaID != initial.PersonaID {
				t.Fatalf("persisted persona=%s", saved.SelectedPersonaID)
			}
		})
	}
}

func TestBareNameSwitchAcknowledgementKeepsTransferredCompaction(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	canonical := []brain.Turn{{Role: "user", Content: "old request"}, {Role: "assistant", Content: "old reply"}, {Role: "user", Content: "recent request"}}
	working := []brain.Turn{{Role: "assistant", Content: "Earlier context summary."}, canonical[2]}
	f.old.history, f.old.working = cloneTestTurns(canonical), cloneTestTurns(working)
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: "Santa", Bare: true}, nil
	})
	f.pipeline.AdmitInput = f.selector.Route
	f.pipeline.SetOutputMuted(true)
	f.pipeline.Events = events.NewBus()
	f.pipeline.OnTurn = func() {
		if err := f.store.Save(f.session, f.pipeline.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	if err := f.pipeline.RunTurnTextMode(t.Context(), "Santa."); err != nil {
		t.Fatal(err)
	}
	snapshot := f.pipeline.Brain.(brain.ContinuityTransferProvider).ContinuitySnapshot()
	if len(snapshot.Canonical) != len(canonical)+2 || !reflect.DeepEqual(snapshot.Canonical[:len(canonical)], canonical) {
		t.Fatalf("canonical=%+v", snapshot.Canonical)
	}
	if len(snapshot.Working) != len(working)+2 || !reflect.DeepEqual(snapshot.Working[:len(working)], working) {
		t.Fatalf("working=%+v", snapshot.Working)
	}
	if last := snapshot.Working[len(snapshot.Working)-1]; last.Role != "assistant" || last.PersonaID != "santa" {
		t.Fatalf("ack=%+v", last)
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) != len(canonical)+2 || saved.SelectedPersonaID != "santa" {
		t.Fatalf("saved=%+v", saved)
	}
}

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

func TestExactControlNegativeCasesStillInvokeResolver(t *testing.T) {
	for _, input := range []string{`"Santa"`, "switch to Santa and tell me a story", "Santa, what do you think?"} {
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
	before := f.selector.Snapshot()
	calls := 0
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		calls++
		return brain.Addressee{Kind: "none"}, nil
	})
	_, err := f.selector.Route(t.Context(), "switch to Unknown", false)
	var unknown *summon.UnknownTargetError
	if !errors.As(err, &unknown) || calls != 0 || f.selector.Snapshot() != before {
		t.Fatalf("err=%v calls=%d snapshot=%+v before=%+v", err, calls, f.selector.Snapshot(), before)
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
