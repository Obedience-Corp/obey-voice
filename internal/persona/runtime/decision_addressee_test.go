package personaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	var classifier atomic.Int32
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		classifier.Add(1)
		return brain.Addressee{Kind: "address", Name: "Santa"}, nil
	})
	got, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err != nil || got.Envelope.PersonaID != "santa" || server.calls.Load() != 1 || classifier.Load() != 1 {
		t.Fatalf("admission=%+v err=%v decision=%d classifier=%d", got, err, server.calls.Load(), classifier.Load())
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

func TestDecisionModelDownWithoutOllamaReportsRetryableFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   admission.FailureCategory
	}{
		{name: "unavailable", status: http.StatusServiceUnavailable, want: admission.FailureClassifierUnavailable},
		{name: "invalid output", status: http.StatusOK, body: "not json", want: admission.FailureClassifierInvalidOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
			defer f.close(t)
			server := newDecisionServer(t, nil)
			server.status, server.body = tc.status, tc.body
			server.enable(t, f.pipeline)
			before := f.selector.Snapshot()
			_, err := f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
			if err == nil || failureCategory(t, err) != tc.want || f.selector.Snapshot() != before {
				t.Fatalf("err=%v selection=%+v", err, f.selector.Snapshot())
			}
		})
	}
}

func TestDecisionModelTimeoutWithoutOllamaReportsLocalTimeout(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	provider, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: slow.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	f.pipeline.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: provider}, Budget: 40 * time.Millisecond, Addressee: true})
	defer f.pipeline.Decisions.Close()
	_, err = f.selector.Route(t.Context(), "Could we hear from Santa on that?", false)
	if err == nil || failureCategory(t, err) != admission.FailureClassifierLocalTimeout {
		t.Fatalf("err=%v", err)
	}
}

func TestDecisionModelAbstainStaysWithCurrentPersona(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	server := newDecisionServer(t, nil)
	server.body = `{"answers":{"target":{"choice":"person_1","confidence":0.4,"probabilities":{"none":0.2,"ambiguous":0.1,"unavailable":0.1,"person_0":0.2,"person_1":0.4}}}}`
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

	other := newDecisionServer(t, nil)
	other.status = http.StatusServiceUnavailable
	other.enable(t, f.pipeline)
	if _, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "obey", Content: text}, nil); accepted || err == nil {
		t.Fatalf("handoff with a down decision model: accepted=%v err=%v", accepted, err)
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
