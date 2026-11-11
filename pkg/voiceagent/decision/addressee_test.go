package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
)

func addresseeInput(account, text string) AddresseeInput {
	return AddresseeInput{
		Participants: []AddresseeParticipant{{Name: "Mira", Aliases: []string{"Meera"}}, {Name: "Jarvis"}},
		Current:      "Jarvis", Speaker: "Lance", Account: account, Text: text,
	}
}

func addresseeAnswer(choice string, confidence float64, bare float64) Response {
	probs := map[string]float64{"none": 0, "ambiguous": 0, "unavailable": 0, "person_0": 0, "person_1": 0}
	probs[choice] = 1
	return Response{Answers: map[string]Answer{
		"target": {Choice: choice, Confidence: &confidence, Probabilities: probs},
		"bare":   {Noul: &bare},
	}}
}

func TestAddresseeRequestShape(t *testing.T) {
	request := AddresseeRequest(addresseeInput("user", "hi"), "guidance text")
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "guidance text") || strings.Contains(string(body), `"addressee"`) {
		t.Fatalf("private request fields leaked: %s", body)
	}
	criteria := request.Questions["target"].Criteria
	for _, key := range []string{"none", "ambiguous", "unavailable", "person_0", "person_1"} {
		if criteria[key] == "" {
			t.Fatalf("missing criterion %q", key)
		}
	}
	if !strings.Contains(criteria["person_0"], "Mira") || !strings.Contains(criteria["person_0"], "Meera") || request.Questions["bare"].Type != "noul" {
		t.Fatalf("questions = %+v", request.Questions)
	}
}

func TestNormalizeAddresseeRules(t *testing.T) {
	in := addresseeInput("user", "x")
	for _, tc := range []struct {
		name    string
		in      AddresseeInput
		raw     Addressee
		want    Addressee
		invalid bool
	}{
		{name: "none drops stray name and bare", in: in, raw: Addressee{Kind: "none", Name: "Mira", Bare: true}, want: Addressee{Kind: "none"}},
		{name: "address", in: in, raw: Addressee{Kind: "address", Name: " mira ", Bare: true}, want: Addressee{Kind: "address", Name: "mira", Bare: true}},
		{name: "alias is allowed", in: in, raw: Addressee{Kind: "address", Name: "Meera"}, want: Addressee{Kind: "address", Name: "Meera"}},
		{name: "outside roster becomes none", in: in, raw: Addressee{Kind: "address", Name: "Charles"}, want: Addressee{Kind: "none"}},
		{name: "empty address invalid", in: in, raw: Addressee{Kind: "address"}, invalid: true},
		{name: "unknown kind invalid", in: in, raw: Addressee{Kind: "oops"}, invalid: true},
		{name: "ambiguous clears bare", in: in, raw: Addressee{Kind: "ambiguous", Name: "Mira", Bare: true}, want: Addressee{Kind: "ambiguous", Name: "Mira"}},
		{name: "agent is never bare", in: addresseeInput("agent", "x"), raw: Addressee{Kind: "address", Name: "Mira", Bare: true}, want: Addressee{Kind: "address", Name: "Mira"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAddressee(tc.raw, tc.in)
			if tc.invalid {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v err %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestInterpretAddresseeProbabilities(t *testing.T) {
	user := addresseeInput("user", "What do you think, Mira?")
	for _, tc := range []struct {
		name     string
		in       AddresseeInput
		response Response
		want     Addressee
		accepted bool
		invalid  bool
	}{
		{name: "address", in: user, response: addresseeAnswer("person_0", 1, 0), want: Addressee{Kind: "address", Name: "Mira"}, accepted: true},
		{name: "bare greeting", in: user, response: addresseeAnswer("person_1", 1, .95), want: Addressee{Kind: "address", Name: "Jarvis", Bare: true}, accepted: true},
		{name: "uncertain bare still addresses without it", in: user, response: addresseeAnswer("person_1", 1, .6), want: Addressee{Kind: "address", Name: "Jarvis"}, accepted: true},
		{name: "agent never bare", in: addresseeInput("agent", "Mira?"), response: addresseeAnswer("person_0", 1, 1), want: Addressee{Kind: "address", Name: "Mira"}, accepted: true},
		{name: "none", in: user, response: addresseeAnswer("none", 1, 0), want: Addressee{Kind: "none"}, accepted: true},
		{name: "absent person is none", in: user, response: addresseeAnswer("unavailable", 1, 0), want: Addressee{Kind: "none"}, accepted: true},
		{name: "ambiguous", in: user, response: addresseeAnswer("ambiguous", 1, 0), want: Addressee{Kind: "ambiguous"}, accepted: true},
		{name: "low confidence abstains", in: user, response: addresseeAnswer("person_0", .5, 0)},
		{name: "unknown choice", in: user, response: addresseeAnswer("person_9", 1, 0), invalid: true},
		{name: "missing bare for user address", in: user, response: func() Response { r := addresseeAnswer("person_0", 1, 0); delete(r.Answers, "bare"); return r }(), invalid: true},
		{name: "missing target", in: user, response: Response{}, invalid: true},
		{name: "incomplete distribution", in: user, response: func() Response {
			r := addresseeAnswer("none", 1, 0)
			a := r.Answers["target"]
			delete(a.Probabilities, "person_1")
			r.Answers["target"] = a
			return r
		}(), invalid: true},
		{name: "distribution not summing", in: user, response: func() Response {
			r := addresseeAnswer("none", 1, 0)
			r.Answers["target"].Probabilities["person_0"] = .5
			return r
		}(), invalid: true},
		{name: "narrow margin abstains", in: user, response: func() Response {
			r := addresseeAnswer("person_0", 1, 0)
			p := r.Answers["target"].Probabilities
			p["person_0"], p["none"] = .55, .45
			return r
		}()},
		{name: "structured address", in: user, response: Response{StructuredAddressee: &Addressee{Kind: "address", Name: "Mira"}}, want: Addressee{Kind: "address", Name: "Mira"}, accepted: true},
		{name: "structured name outside roster", in: user, response: Response{StructuredAddressee: &Addressee{Kind: "address", Name: "Charles"}}, want: Addressee{Kind: "none"}, accepted: true},
		{name: "structured empty address", in: user, response: Response{StructuredAddressee: &Addressee{Kind: "address"}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, accepted, err := InterpretAddressee(tc.in, tc.response)
			if tc.invalid {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || accepted != tc.accepted || (tc.accepted && got != tc.want) {
				t.Fatalf("got %+v accepted=%v err=%v", got, accepted, err)
			}
		})
	}
}

func systemOneServer(t *testing.T, calls *atomic.Int32, handle func(request map[string]any) (int, any)) *SystemOne {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		code, payload := handle(body)
		w.WriteHeader(code)
		if raw, ok := payload.(string); ok {
			_, _ = io.WriteString(w, raw)
			return
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	provider, err := NewSystemOne(SystemOneConfig{Provider: "local", Endpoint: server.URL + "/v1/systemone"})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func enabledController(t *testing.T, evaluator Evaluator, budget time.Duration) *Controller {
	t.Helper()
	var c Controller
	c.Configure(Options{Policy: WorkPolicy{Evaluator: evaluator}, Budget: budget, Addressee: true})
	t.Cleanup(c.Close)
	return &c
}

func TestSystemOneAddresseeQuestion(t *testing.T) {
	var calls atomic.Int32
	provider := systemOneServer(t, &calls, func(body map[string]any) (int, any) {
		questions, _ := body["questions"].(map[string]any)
		state, _ := body["state"].(map[string]any)
		if questions["target"] == nil || questions["bare"] == nil || state["text"] != "What do you think, Mira?" || body["addressee"] != nil || body["guidance"] != nil {
			t.Errorf("unexpected body %v", body)
		}
		return 200, map[string]any{"model": "decider", "answers": map[string]any{
			"target": map[string]any{"choice": "person_0", "confidence": 0.97, "probabilities": map[string]float64{"none": 0.01, "ambiguous": 0.01, "unavailable": 0.005, "person_0": 0.97, "person_1": 0.005}},
			"bare":   map[string]any{"noul": 0.02},
		}}
	})
	c := enabledController(t, provider, time.Second)
	got, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "What do you think, Mira?"), "guidance")
	if err != nil || !ok || got != (Addressee{Kind: "address", Name: "Mira"}) || calls.Load() != 1 {
		t.Fatalf("got %+v ok=%v err=%v calls=%d", got, ok, err, calls.Load())
	}
}

func TestSystemOneAddresseeFailuresMapToClassifierKinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   admission.ClassifierFailureKind
		handle func(map[string]any) (int, any)
		budget time.Duration
	}{
		{name: "http error", kind: admission.ClassifierUnavailable, handle: func(map[string]any) (int, any) { return 503, "down" }, budget: time.Second},
		{name: "malformed body", kind: admission.ClassifierInvalidOutput, handle: func(map[string]any) (int, any) { return 200, "not json" }, budget: time.Second},
		{name: "contract violation", kind: admission.ClassifierInvalidOutput, handle: func(map[string]any) (int, any) { return 200, map[string]any{"answers": map[string]any{}} }, budget: time.Second},
		{name: "slow", kind: admission.ClassifierLocalTimeout, handle: func(map[string]any) (int, any) { time.Sleep(300 * time.Millisecond); return 200, "{}" }, budget: 50 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := enabledController(t, systemOneServer(t, &calls, tc.handle), tc.budget)
			_, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "Mira what now"), "")
			var failure *admission.ClassifierFailure
			if ok || !errors.As(err, &failure) || failure.Kind != tc.kind {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestSystemOneAddresseeTransportDown(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL + "/v1/systemone"
	server.Close()
	provider, err := NewSystemOne(SystemOneConfig{Provider: "local", Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	c := enabledController(t, provider, time.Second)
	_, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "Mira what now"), "")
	var failure *admission.ClassifierFailure
	if ok || !errors.As(err, &failure) || failure.Kind != admission.ClassifierUnavailable {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestOpenAIAddresseeQuestion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    Addressee
		invalid bool
	}{
		{name: "address", content: `{"kind":"address","name":"Mira","bare":false}`, want: Addressee{Kind: "address", Name: "Mira"}},
		{name: "outside roster", content: `{"kind":"address","name":"Charles","bare":false}`, want: Addressee{Kind: "none"}},
		{name: "none with stray name", content: `{"kind":"none","name":"Mira","bare":true}`, want: Addressee{Kind: "none"}},
		{name: "empty address", content: `{"kind":"address","name":"","bare":false}`, invalid: true},
		{name: "unknown kind", content: `{"kind":"maybe","name":"","bare":false}`, invalid: true},
		{name: "extra field", content: `{"kind":"none","name":"","bare":false,"x":1}`, invalid: true},
		{name: "malformed", content: `{"kind":`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				format := body["text"].(map[string]any)["format"].(map[string]any)
				properties := format["schema"].(map[string]any)["properties"].(map[string]any)
				names := properties["name"].(map[string]any)["enum"].([]any)
				if format["name"] != "addressee_decision" || format["strict"] != true || len(names) != 4 || body["instructions"] != "private guidance" {
					t.Fatalf("request = %v", body)
				}
				if input, _ := body["input"].(string); !strings.Contains(input, `"text":"What do you think, Mira?"`) || strings.Contains(input, "questions") {
					t.Fatalf("input = %v", body["input"])
				}
				raw, _ := json.Marshal(map[string]any{"model": "m", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": tc.content}}}}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
			})}
			provider, err := NewOpenAI(OpenAIConfig{Model: "m", APIKey: "k", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			c := enabledController(t, provider, time.Second)
			got, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "What do you think, Mira?"), "private guidance")
			if tc.invalid {
				var failure *admission.ClassifierFailure
				if !errors.As(err, &failure) || failure.Kind != admission.ClassifierInvalidOutput || ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				return
			}
			if err != nil || !ok || got != tc.want || calls != 1 {
				t.Fatalf("got %+v ok=%v err=%v calls=%d", got, ok, err, calls)
			}
		})
	}
}

func TestOpenAIAddresseeHTTPErrorIsUnavailable(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	provider, _ := NewOpenAI(OpenAIConfig{Model: "m", APIKey: "k", Client: client})
	_, ok, err := enabledController(t, provider, time.Second).ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), "")
	var failure *admission.ClassifierFailure
	if ok || !errors.As(err, &failure) || failure.Kind != admission.ClassifierUnavailable {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestResolveAddresseeOffAndIneligibleMakeNoCall(t *testing.T) {
	var calls atomic.Int32
	evaluator := evaluatorFunc(func(context.Context, Request) (Response, error) { calls.Add(1); return Response{}, nil })
	var zero Controller
	if _, ok, err := zero.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), ""); ok || err != nil {
		t.Fatal("zero controller answered")
	}
	var nilController *Controller
	if _, ok, err := nilController.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), ""); ok || err != nil {
		t.Fatal("nil controller answered")
	}
	workOnly := &Controller{}
	workOnly.Configure(Options{Policy: WorkPolicy{Evaluator: evaluator}, Budget: time.Second, VoiceWork: true})
	defer workOnly.Close()
	if workOnly.Enabled(AddresseeResolution) {
		t.Fatal("work-only options enabled addressee")
	}
	c := enabledController(t, evaluator, time.Second)
	for _, in := range []AddresseeInput{{Text: "Mira?"}, addresseeInput("user", ""), addresseeInput("user", strings.Repeat("a", 4001))} {
		if _, ok, err := c.ResolveAddressee(t.Context(), in, ""); ok || err != nil {
			t.Fatalf("ineligible input answered: %+v", in)
		}
	}
	if _, ok, _ := workOnly.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), ""); ok || calls.Load() != 0 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestResolveAddresseeIsIndependentOfWorkInFlight(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	evaluator := evaluatorFunc(func(ctx context.Context, r Request) (Response, error) {
		if r.addressee == nil {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return certain(Simple), nil
		}
		return addresseeAnswer("person_0", 1, 0), nil
	})
	var c Controller
	c.Configure(Options{Policy: WorkPolicy{Evaluator: evaluator, Mode: PolicyIntentAndTier}, Budget: time.Second, VoiceWork: true, Addressee: true})
	defer c.Close()
	attempt := c.Start(t.Context(), VoiceWork, sampleInput())
	if attempt == nil {
		t.Fatal("work attempt refused")
	}
	defer attempt.Close()
	<-entered
	got, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "What do you think, Mira?"), "")
	close(release)
	if err != nil || !ok || got.Name != "Mira" {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
}

func TestResolveAddresseeCancellation(t *testing.T) {
	entered := make(chan struct{})
	evaluator := evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(entered)
		<-ctx.Done()
		return Response{}, ctx.Err()
	})
	c := enabledController(t, evaluator, 5*time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-entered; cancel() }()
	_, ok, err := c.ResolveAddressee(ctx, addresseeInput("user", "Mira?"), "")
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var failure *admission.ClassifierFailure
	if errors.As(err, &failure) {
		t.Fatal("parent cancellation reported as classifier failure")
	}
}

func TestResolveAddresseeDisabledMidCallDiscardsResult(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	evaluator := evaluatorFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return addresseeAnswer("person_0", 1, 0), nil
	})
	c := enabledController(t, evaluator, 5*time.Second)
	go func() { <-entered; c.Close(); close(release) }()
	if _, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), ""); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestResolveAddresseeAllowsOneCallInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	evaluator := evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return Response{}, errors.New("late")
	})
	c := enabledController(t, evaluator, 50*time.Millisecond)
	_, _, _ = c.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), "")
	<-entered
	if _, ok, err := c.ResolveAddressee(t.Context(), addresseeInput("user", "Mira?"), ""); ok || err != nil || calls.Load() != 1 {
		t.Fatalf("second call ok=%v err=%v calls=%d", ok, err, calls.Load())
	}
	close(release)
}
