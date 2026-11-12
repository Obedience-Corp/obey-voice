package personaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// decisionServer answers the addressee question from a table keyed by the
// message text. An unlisted text gets the "none" answer; status overrides.
type decisionServer struct {
	calls   atomic.Int32
	answers map[string]string
	status  int
	body    string
	server  *httptest.Server
}

func newDecisionServer(t *testing.T, answers map[string]string) *decisionServer {
	t.Helper()
	d := &decisionServer{answers: answers, status: http.StatusOK}
	d.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.calls.Add(1)
		var req struct {
			State struct {
				Text         string `json:"text"`
				Participants []struct {
					Name string `json:"name"`
				} `json:"participants"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if d.status != http.StatusOK || d.body != "" {
			w.WriteHeader(d.status)
			_, _ = w.Write([]byte(d.body))
			return
		}
		choice := d.answers[req.State.Text]
		if choice == "" {
			choice = "none"
		}
		probs := map[string]float64{"none": 0, "ambiguous": 0, "unavailable": 0}
		want := choice
		for i, p := range req.State.Participants {
			key := "person_" + string(rune('0'+i))
			probs[key] = 0
			if strings.EqualFold(choice, p.Name) {
				want = key
			}
		}
		probs[want] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{
			"target": map[string]any{"choice": want, "confidence": 1.0, "probabilities": probs},
			"bare":   map[string]any{"noul": 0.0},
		}})
	}))
	t.Cleanup(d.server.Close)
	return d
}

func (d *decisionServer) enable(t *testing.T, p *pipeline.Pipeline) {
	t.Helper()
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: d.server.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	p.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: time.Second, Addressee: true})
	t.Cleanup(p.Decisions.Close)
}

func failureCategory(t *testing.T, err error) admission.FailureCategory {
	t.Helper()
	category, ok := admission.FailureCategoryOf(err)
	if !ok {
		t.Fatalf("not an admission failure: %v", err)
	}
	return category
}

func TestDecisionModelResolvesAddresseeWithoutOllama(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	if _, ok := f.pipeline.Brain.(brain.AddresseeResolver); ok {
		t.Fatal("fixture brain has a provider classifier")
	}
	server := newDecisionServer(t, map[string]string{"Could we hear from Santa on that?": "Santa"})
	server.enable(t, f.pipeline)

	got, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err != nil || got.Envelope.PersonaID != "santa" || got.Request != "Could we hear from Santa on that?" || server.calls.Load() != 1 {
		t.Fatalf("admission=%+v err=%v calls=%d", got, err, server.calls.Load())
	}

	got, err = f.selector.Route(t.Context(), "What did Jarvis say a minute ago?", false)
	if err != nil || got.Envelope.PersonaID != "santa" || server.calls.Load() != 2 {
		t.Fatalf("mention changed the responder: %+v err=%v calls=%d", got, err, server.calls.Load())
	}
}

func TestDecisionModelAmbiguousIsRefusedAsToday(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, map[string]string{"Could Jarvis and Santa weigh in on that?": "ambiguous"})
	server.enable(t, f.pipeline)
	before := f.selector.Snapshot()
	_, err := f.selector.Route(t.Context(), "Could Jarvis and Santa weigh in on that?", false)
	if err == nil || failureCategory(t, err) != admission.FailureIdentityRefusal || f.selector.Snapshot() != before {
		t.Fatalf("err=%v selection=%+v", err, f.selector.Snapshot())
	}
}

func TestDeterministicGrammarMakesNoDecisionCall(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, nil)
	server.enable(t, f.pipeline)
	for input, want := range map[string]string{
		"Santa, what do you think?": "santa",
		"switch to Jarvis":          "jarvis",
		"@santa hello":              "santa",
		"Hey Jarvis":                "jarvis",
	} {
		got, err := f.selector.Route(t.Context(), input, false)
		if err != nil || got.Envelope.PersonaID != want {
			t.Fatalf("%q: %+v err=%v", input, got, err)
		}
	}
	if server.calls.Load() != 0 {
		t.Fatalf("deterministic routes called the decision model %d times", server.calls.Load())
	}
	if _, err := f.selector.Route(t.Context(), "Nothing about the roster here", false); err != nil || server.calls.Load() != 0 {
		t.Fatalf("no candidate name still called the model: err=%v calls=%d", err, server.calls.Load())
	}
}

func TestDecisionModelDownFallsBackToOllamaClassifier(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, nil)
	server.status = http.StatusServiceUnavailable
	server.enable(t, f.pipeline)
	watch := watchRouting(f.pipeline)
	var classifier atomic.Int32
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		classifier.Add(1)
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	got, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err != nil || got.Envelope.PersonaID != "santa" || server.calls.Load() != 1 || classifier.Load() != 1 {
		t.Fatalf("admission=%+v err=%v decision=%d classifier=%d", got, err, server.calls.Load(), classifier.Load())
	}
	infos, diagnostics := watch.snapshot()
	if len(infos) != 0 || len(diagnostics) != 1 || diagnostics[0].Resolver != "ollama_classifier" || diagnostics[0].Disposition != "provider_error" {
		t.Fatalf("infos=%q diagnostics=%+v", infos, diagnostics)
	}
}

// The decision model sits in front of the Ollama classifier, so without a
// shared bound a slow decider plus a slow classifier would wait for
// decision_budget_ms + persona_routing_timeout.
func TestAddresseeResolutionStaysWithinPersonaRoutingTimeout(t *testing.T) {
	t.Parallel()
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	f.selector.opts.Base.PersonaRoutingTimeout = 1
	watch := watchRouting(f.pipeline)
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: slow.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: 500 * time.Millisecond, Addressee: true})
	defer f.pipeline.Decisions.Close()
	var classifierDeadline time.Time
	// Mirrors OllamaBrain.ResolveAddressee: its own persona_routing_timeout,
	// and a parent error that is reported unclassified.
	f.selector.opts.AddressResolver = addressResolverFunc(func(ctx context.Context, _ brain.AddressRequest) (brain.Addressee, error) {
		classifierDeadline, _ = ctx.Deadline()
		local, cancel := context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(f.selector.opts.Base))
		defer cancel()
		<-local.Done()
		if err := ctx.Err(); err != nil {
			return brain.Addressee{}, fmt.Errorf("resolve conversation addressee: %w", err)
		}
		return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, local.Err())
	})
	started := time.Now()
	_, err = f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	elapsed := time.Since(started)
	if err == nil || failureCategory(t, err) != admission.FailureClassifierLocalTimeout {
		t.Fatalf("err=%v", err)
	}
	// Unbounded, the classifier gets no deadline from the selector and the
	// question takes about 0.5 s + 1 s.
	if classifierDeadline.IsZero() || classifierDeadline.Sub(started) > time.Second+300*time.Millisecond {
		t.Fatalf("classifier deadline %v after start, want within persona_routing_timeout", classifierDeadline.Sub(started))
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("elapsed = %v, want about persona_routing_timeout (1s)", elapsed)
	}
	t.Logf("decision budget 500ms + classifier timeout 1s resolved in %v", elapsed)
	if _, diagnostics := watch.snapshot(); len(diagnostics) != 1 || diagnostics[0].Resolver != "failed" || diagnostics[0].Disposition != "timeout" || diagnostics[0].Elapsed < 900*time.Millisecond {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

// A decision model that spends the whole persona_routing_timeout leaves the
// classifier a dead context; the turn must fall back without calling it.
func TestDecisionModelUsingWholeTimeoutSkipsClassifier(t *testing.T) {
	t.Parallel()
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	f.selector.opts.Base.PersonaRoutingTimeout = 1
	watch := watchRouting(f.pipeline)
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: slow.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: time.Second, Addressee: true})
	defer f.pipeline.Decisions.Close()
	var classifier atomic.Int32
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		classifier.Add(1)
		return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, context.DeadlineExceeded)
	})
	got, err := f.selector.Route(t.Context(), "Santa tell me a story", false)
	if err != nil || got.Envelope.PersonaID != "santa" || classifier.Load() != 0 {
		t.Fatalf("admission=%+v err=%v classifier calls=%d", got, err, classifier.Load())
	}
	infos, diagnostics := watch.snapshot()
	if len(infos) != 1 || !strings.Contains(infos[0], "timed out") || len(diagnostics) != 1 || diagnostics[0].Resolver != "grammar" || diagnostics[0].Disposition != "timeout" {
		t.Fatalf("infos=%q diagnostics=%+v", infos, diagnostics)
	}
}

const abstainBody = `{"answers":{"target":{"choice":"person_1","confidence":0.4,"probabilities":{"none":0.2,"ambiguous":0.1,"unavailable":0.1,"person_0":0.2,"person_1":0.4}}}}`

// lateAbstainer abstains just before its deadline, leaving the classifier less
// than minClassifierWindow.
type lateAbstainer struct{ left time.Duration }

func (e lateAbstainer) Evaluate(ctx context.Context, _ decision.Request) (decision.Response, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return decision.Response{}, errors.New("no deadline")
	}
	timer := time.NewTimer(time.Until(deadline) - e.left)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return decision.Response{}, ctx.Err()
	}
	var response decision.Response
	err := json.Unmarshal([]byte(abstainBody), &response)
	return response, err
}

// An abstention can use up persona_routing_timeout as surely as a failure; the
// classifier must not then be called with the time that is left.
func TestDecisionModelAbstainingAtTheDeadlineSkipsClassifier(t *testing.T) {
	t.Parallel()
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	f.selector.opts.Base.PersonaRoutingTimeout = 1
	watch := watchRouting(f.pipeline)
	// A budget above the routing timeout leaves the routing timeout as the
	// decision call's deadline.
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: lateAbstainer{left: 40 * time.Millisecond}}, Budget: 2 * time.Second, Addressee: true})
	defer f.pipeline.Decisions.Close()
	var classifier atomic.Int32
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		classifier.Add(1)
		return brain.Addressee{}, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, context.DeadlineExceeded)
	})
	got, err := f.selector.Route(t.Context(), "Santa tell me a story", false)
	if err != nil || got.Envelope.PersonaID != "santa" || classifier.Load() != 0 {
		t.Fatalf("admission=%+v err=%v classifier calls=%d", got, err, classifier.Load())
	}
	infos, diagnostics := watch.snapshot()
	if len(infos) != 0 || len(diagnostics) != 1 || diagnostics[0].Resolver != "grammar" || diagnostics[0].Disposition != "abstained" {
		t.Fatalf("infos=%q diagnostics=%+v", infos, diagnostics)
	}
}

func TestAddresseeDiagnosticNamesTheResolver(t *testing.T) {
	for _, tc := range []struct {
		name, input, body     string
		status                int
		ollama, decisionOff   bool
		resolver, disposition string
		wantPersona           string
	}{
		{name: "decision model", input: "Could we hear from Santa on that?", resolver: "decision_model", disposition: "answered", wantPersona: "santa"},
		{name: "decision says none", input: "What did Santa say?", resolver: "decision_model", disposition: "answered", wantPersona: "jarvis"},
		{name: "abstain then grammar", input: "Santa tell me a story", body: abstainBody, resolver: "grammar", disposition: "abstained", wantPersona: "santa"},
		{name: "abstain then current", input: "Could we hear from Santa on that?", body: abstainBody, resolver: "current_persona", disposition: "abstained", wantPersona: "jarvis"},
		{name: "failure then classifier", input: "Could we hear from Santa on that?", status: http.StatusServiceUnavailable, ollama: true, resolver: "ollama_classifier", disposition: "provider_error", wantPersona: "santa"},
		{name: "classifier only", input: "Could we hear from Santa on that?", decisionOff: true, ollama: true, resolver: "ollama_classifier", disposition: "not_asked", wantPersona: "santa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			defer f.close(t)
			watch := watchRouting(f.pipeline)
			if !tc.decisionOff {
				server := newDecisionServer(t, map[string]string{"Could we hear from Santa on that?": "Santa"})
				if tc.status != 0 {
					server.status = tc.status
				}
				server.body = tc.body
				server.enable(t, f.pipeline)
			}
			if tc.ollama {
				f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
					return brain.Addressee{Kind: "address", Name: "Santa"}, nil
				})
			}
			got, err := f.selector.Route(t.Context(), tc.input, false)
			if err != nil || got.Envelope.PersonaID != tc.wantPersona {
				t.Fatalf("admission=%+v err=%v", got, err)
			}
			_, diagnostics := watch.snapshot()
			if len(diagnostics) != 1 || diagnostics[0].Resolver != tc.resolver || diagnostics[0].Disposition != tc.disposition || diagnostics[0].Elapsed <= 0 {
				t.Fatalf("diagnostics = %+v", diagnostics)
			}
		})
	}
}

func TestDecisionModelAnswerSkipsOllamaClassifier(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, map[string]string{"Could we hear from Santa on that?": "Santa"})
	server.enable(t, f.pipeline)
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		t.Error("classifier called after the decision model answered")
		return brain.Addressee{}, errors.New("unexpected")
	})
	got, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err != nil || got.Envelope.PersonaID != "santa" {
		t.Fatalf("admission=%+v err=%v", got, err)
	}
}

// stuckEvaluator ignores cancellation until released, like a provider whose
// HTTP stack does not honour the deadline.
type stuckEvaluator struct {
	calls   atomic.Int32
	release chan struct{}
}

func (e *stuckEvaluator) Evaluate(context.Context, decision.Request) (decision.Response, error) {
	e.calls.Add(1)
	<-e.release
	return decision.Response{}, errors.New("released")
}

type routingEvents struct {
	mu        sync.Mutex
	infos     []string
	addressee []events.DecisionEvaluated
}

func watchRouting(p *pipeline.Pipeline) *routingEvents {
	if p.Events == nil {
		p.Events = events.NewBus()
	}
	w := &routingEvents{}
	events.Subscribe(p.Events, func(e events.Info) { w.mu.Lock(); w.infos = append(w.infos, e.Message); w.mu.Unlock() })
	events.Subscribe(p.Events, func(e events.DecisionEvaluated) {
		if e.Point == "addressee" {
			w.mu.Lock()
			w.addressee = append(w.addressee, e)
			w.mu.Unlock()
		}
	})
	return w
}

func (w *routingEvents) snapshot() ([]string, []events.DecisionEvaluated) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.infos), slices.Clone(w.addressee)
}

func TestDecisionModelFailureWithoutOllamaKeepsNameGrammar(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, reason string
		setup                 func(t *testing.T, p *pipeline.Pipeline)
	}{
		{name: "unavailable", outcome: "provider_error", reason: "its provider returned an error", setup: func(t *testing.T, p *pipeline.Pipeline) {
			server := newDecisionServer(t, nil)
			server.status = http.StatusServiceUnavailable
			server.enable(t, p)
		}},
		{name: "invalid output", outcome: "invalid_output", reason: "it gave an answer that could not be used", setup: func(t *testing.T, p *pipeline.Pipeline) {
			server := newDecisionServer(t, nil)
			server.body = "not json"
			server.enable(t, p)
		}},
		{name: "timeout", outcome: "timeout", reason: "it timed out", setup: func(t *testing.T, p *pipeline.Pipeline) {
			release := make(chan struct{})
			slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
			t.Cleanup(slow.Close)
			t.Cleanup(func() { close(release) })
			provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: slow.URL + "/v1/systemone"})
			if err != nil {
				t.Fatal(err)
			}
			p.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: 40 * time.Millisecond, Addressee: true})
			t.Cleanup(p.Decisions.Close)
		}},
		{name: "busy", outcome: "busy", reason: "it was still busy with an earlier question", setup: func(t *testing.T, p *pipeline.Pipeline) {
			stuck := &stuckEvaluator{release: make(chan struct{})}
			t.Cleanup(func() { close(stuck.release) })
			p.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: stuck}, Budget: 40 * time.Millisecond, Addressee: true})
			t.Cleanup(p.Decisions.Close)
			// The first question times out and leaves its call running.
			_, _ = p.Decisions.ResolveAddressee(t.Context(), decision.AddresseeInput{Participants: []decision.AddresseeParticipant{{Name: "Santa"}}, Text: "Santa?"}, "")
			if stuck.calls.Load() != 1 {
				t.Fatalf("priming calls = %d", stuck.calls.Load())
			}
			t.Cleanup(func() {
				if stuck.calls.Load() != 1 {
					t.Errorf("busy decider was called again: %d", stuck.calls.Load())
				}
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			defer f.close(t)
			watch := watchRouting(f.pipeline)
			tc.setup(t, f.pipeline)

			// Without a decision model or Ollama this is the grammar's imperative
			// address, so only the grammar can route it to Santa.
			got, err := f.selector.Route(t.Context(), "Santa tell me a story", false)
			if err != nil || got.Envelope.PersonaID != "santa" || got.Request != "tell me a story" {
				t.Fatalf("admission=%+v err=%v", got, err)
			}
			got, err = f.selector.Route(t.Context(), "Could we hear from Jarvis on that?", false)
			if err != nil || got.Envelope.PersonaID != "santa" {
				t.Fatalf("grammar miss did not stay with the current persona: %+v err=%v", got, err)
			}
			infos, diagnostics := watch.snapshot()
			if len(infos) != 2 {
				t.Fatalf("want one notice per turn, got %q", infos)
			}
			for _, info := range infos {
				if !strings.Contains(info, "did not answer") || !strings.Contains(info, tc.reason) || strings.Contains(info, "_") || strings.Contains(info, "(") || strings.Contains(info, "story") || strings.Contains(info, "hear from") {
					t.Fatalf("notice = %q", info)
				}
			}
			if len(diagnostics) != 2 || diagnostics[0].Resolver != "grammar" || diagnostics[1].Resolver != "current_persona" ||
				diagnostics[0].Disposition != tc.outcome || diagnostics[1].Disposition != tc.outcome {
				t.Fatalf("diagnostics = %+v", diagnostics)
			}
		})
	}
}

func TestAddresseeFailureReasonBlamesTheProviderOnlyForProviderErrors(t *testing.T) {
	if got := addresseeFailureReason(decision.AddresseeProviderError); got != "its provider returned an error" {
		t.Fatalf("provider error reason = %q", got)
	}
	for _, outcome := range []decision.AddresseeOutcome{decision.AddresseeCancelled, decision.AddresseeSkipped, "not_yet_defined"} {
		if got := addresseeFailureReason(outcome); got != "the question did not complete" {
			t.Errorf("%s reason = %q, want the neutral phrase", outcome, got)
		}
	}
}

func TestTurnCancellationDuringDecisionStillCancels(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	watch := watchRouting(f.pipeline)
	entered := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer slow.Close()
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: slow.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: 3 * time.Second, Addressee: true})
	defer f.pipeline.Decisions.Close()
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-entered; cancel() }()
	before := f.selector.Snapshot()
	_, err = f.selector.Route(ctx, "Santa tell me a story", false)
	if err == nil || failureCategory(t, err) != admission.FailureParentCancellation || f.selector.Snapshot() != before {
		t.Fatalf("err=%v selection=%+v", err, f.selector.Snapshot())
	}
	if infos, _ := watch.snapshot(); len(infos) != 0 {
		t.Fatalf("cancelled turn produced a notice: %q", infos)
	}
}

func TestDecisionModelAbstainStaysWithCurrentPersona(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, nil)
	server.body = abstainBody
	server.enable(t, f.pipeline)
	before := f.selector.Snapshot()
	got, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err != nil || got.Envelope.PersonaID != before.PersonaID || server.calls.Load() != 1 {
		t.Fatalf("admission=%+v err=%v calls=%d", got, err, server.calls.Load())
	}
}

func TestDecisionDisabledKeepsProviderBehavior(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, nil)
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: server.server.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: time.Second, VoiceWork: true})
	defer f.pipeline.Decisions.Close()
	var classifier atomic.Int32
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		classifier.Add(1)
		return brain.Addressee{Kind: "none"}, nil
	})
	if _, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false); err != nil || server.calls.Load() != 0 || classifier.Load() != 1 {
		t.Fatalf("err=%v decision=%d classifier=%d", err, server.calls.Load(), classifier.Load())
	}
}

func TestAgentHandoffUsesDecisionModelWithoutOllama(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("venessa", "Venessa", "venessa"))
	defer f.close(t)
	text := "That is a design question. Venessa, what is your view on the layout?"
	server := newDecisionServer(t, map[string]string{text: "Venessa"})
	server.enable(t, f.pipeline)
	proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "obey", Content: text}, nil)
	if err != nil || !accepted || proposal.PersonaID != "venessa" || server.calls.Load() != 1 {
		t.Fatalf("accepted=%v proposal=%+v err=%v calls=%d", accepted, proposal, err, server.calls.Load())
	}

	// A down decision model leaves the reply with its speaker, as before the
	// decision model existed; only the diagnostic records it.
	watch := watchRouting(f.pipeline)
	other := newDecisionServer(t, nil)
	other.status = http.StatusServiceUnavailable
	other.enable(t, f.pipeline)
	if _, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "obey", Content: text}, nil); accepted || err != nil {
		t.Fatalf("handoff with a down decision model: accepted=%v err=%v", accepted, err)
	}
	infos, diagnostics := watch.snapshot()
	if len(infos) != 0 || len(diagnostics) != 1 || diagnostics[0].Resolver != "current_persona" || diagnostics[0].Disposition != "provider_error" {
		t.Fatalf("notices=%q diagnostics=%+v", infos, diagnostics)
	}
}

// A user turn is routed and then its reply is checked for a handoff; a down
// decision model must not produce a notice for each.
func TestDownDecisionModelGivesOneNoticePerTurn(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	watch := watchRouting(f.pipeline)
	server := newDecisionServer(t, nil)
	server.status = http.StatusUnauthorized
	server.enable(t, f.pipeline)
	got, err := f.selector.Route(t.Context(), "Santa tell me a story", false)
	if err != nil || got.Envelope.PersonaID != "santa" {
		t.Fatalf("admission=%+v err=%v", got, err)
	}
	reply := brain.Turn{Role: "assistant", PersonaID: "santa", Content: "Santa here. Jarvis, do you know a good one?"}
	if _, accepted, err := f.selector.RouteHandoff(t.Context(), reply, nil); accepted || err != nil {
		t.Fatalf("handoff accepted=%v err=%v", accepted, err)
	}
	infos, diagnostics := watch.snapshot()
	if len(infos) != 1 || len(diagnostics) != 2 || server.calls.Load() != 2 {
		t.Fatalf("notices=%q diagnostics=%+v calls=%d", infos, diagnostics, server.calls.Load())
	}
}

func TestDecisionModelWithHarnessBackendNeverCallsIt(t *testing.T) {
	log := installMixedProviderBinaries(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	var ollamaChats atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/chat" {
			ollamaChats.Add(1)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
	}))
	defer ollama.Close()

	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for id, display := range map[string]string{"ember": "Ember", "scout": "Scout"} {
		profile := testProfile(id, display, id)
		profile.Brain.Model = ""
		if id == "scout" {
			profile.Brain.Provider = "muse-spark"
		}
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	base := &config.Config{ActivePersona: "ember", BrainProvider: "ollama", OllamaModel: "fixture:latest", OllamaHost: ollama.URL,
		PromptsDir: promptDir, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192}
	binding, err := catalog.ResolveBinding(base, "ember")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: initial, Events: events.NewBus()}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "fixture:latest")
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		Persist: func(turns []brain.Turn, s events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, s.PersonaID, s.DisplayName, s.PromptHash, s.Directories)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := newDecisionServer(t, map[string]string{"Could we hear from Ember on that?": "Ember", "Could we hear from Scout on that?": "Scout"})
	server.enable(t, p)

	if got, err := selector.Route(t.Context(), "switch to Scout", false); err != nil || got.Envelope.PersonaID != "scout" {
		t.Fatalf("switch: %+v err=%v", got, err)
	}
	if _, ok := p.Brain.(brain.AddresseeResolver); ok {
		t.Fatal("live harness brain exposes a classifier")
	}
	got, err := selector.Route(t.Context(), "Could we hear from Ember on that?", false)
	if err != nil || got.Envelope.PersonaID != "ember" || server.calls.Load() != 1 {
		t.Fatalf("admission=%+v err=%v calls=%d", got, err, server.calls.Load())
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("harness backend was invoked during routing: %v", err)
	}
	if ollamaChats.Load() != 0 {
		t.Fatalf("ollama chat calls = %d", ollamaChats.Load())
	}
}
