package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type evaluatorFunc func(context.Context, Request) (Response, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, r Request) (Response, error) { return f(ctx, r) }

func certain(tier Tier) Response {
	one := 1.0
	probs := map[string]float64{"none": 0, "simple": 0, "medium": 0, "complex": 0}
	probs[string(tier)] = 1
	return Response{Model: "fixture", Answers: map[string]Answer{
		"work": {Noul: &one}, "tier": {Choice: string(tier), Confidence: &one, Probabilities: probs},
	}}
}

func sampleInput() WorkInput {
	return WorkInput{Text: "Check the installed Git version.", EnabledTiers: []Tier{Simple, Medium}}
}

func TestSystemOneLocalBoundary(t *testing.T) {
	for _, endpoint := range []string{"https://api.typesafe.ai/v1/systemone", "http://192.0.2.1/v1/systemone", "http://user:secret@127.0.0.1/v1/systemone", "http://127.0.0.1/v1/systemone?key=secret"} {
		if _, err := NewSystemOne(SystemOneConfig{Provider: "local", Endpoint: endpoint}); err == nil {
			t.Fatalf("accepted nonprivate endpoint %s", endpoint)
		}
	}
	if _, err := NewSystemOne(SystemOneConfig{Provider: "local", APIKey: "secret"}); err == nil {
		t.Fatal("local provider accepted credentials")
	}
	if _, err := NewSystemOne(SystemOneConfig{Provider: "jev", Model: "jev-latest"}); err == nil {
		t.Fatal("jev accepted missing credential")
	}
	if _, err := NewSystemOne(SystemOneConfig{Provider: "jev", Endpoint: "https://example.com/v1/systemone", Model: "m", APIKey: "key"}); err == nil {
		t.Fatal("jev accepted a foreign credential destination")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("local request sent credentials")
		}
		var body struct {
			Model string `json:"model"`
			Request
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "decider" || body.Questions["work"].Type != "noul" {
			t.Error("incorrect wire request")
		}
		_ = json.NewEncoder(w).Encode(certain(Simple))
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		t.Error("local request consulted a proxy")
		return nil, errors.New("proxy used")
	}
	provider, err := NewSystemOne(SystemOneConfig{Provider: "local", Endpoint: server.URL + "/v1/systemone", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (WorkPolicy{Evaluator: provider}).Decide(t.Context(), sampleInput())
	if err != nil || !result.Accepted || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestSystemOneFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"redirect", "http_error", "invalid_json", "timeout", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(302)
				case "http_error":
					w.WriteHeader(503)
				case "invalid_json":
					fmt.Fprint(w, "not JSON")
				case "timeout":
					<-release
				default:
					_ = json.NewEncoder(w).Encode(certain(Simple))
				}
			}))
			defer server.Close()
			defer close(release)
			provider, err := NewSystemOne(SystemOneConfig{Provider: "local", Endpoint: server.URL + "/v1/systemone"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			_, err = provider.Evaluate(ctx, WorkRequest(sampleInput()))
			if err == nil {
				t.Fatal("failure produced a decision")
			}
			if mode == "cancelled" && (calls.Load() != 0 || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancelled: calls=%d err=%v", calls.Load(), err)
			}
			if mode == "redirect" && calls.Load() != 1 {
				t.Fatal("followed redirect")
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost timeout: %v", err)
			}
		})
	}
}

func TestWorkPolicyGuards(t *testing.T) {
	for _, mode := range []string{"success", "off", "unavailable", "uncertain", "missing", "invalid_distribution", "negative_intent", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			policy := WorkPolicy{Evaluator: evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) {
				calls++
				response := certain(Simple)
				if mode == "unavailable" {
					response = certain(Complex)
				}
				if mode == "missing" {
					delete(response.Answers, "work")
				}
				if mode == "invalid_distribution" {
					response.Answers["tier"].Probabilities["medium"] = 1
				}
				if mode == "uncertain" {
					v := .84
					a := response.Answers["tier"]
					a.Confidence = &v
					response.Answers["tier"] = a
				}
				if mode == "negative_intent" {
					v := 0.0
					response.Answers["work"] = Answer{Noul: &v}
				}
				return response, nil
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "off" {
				policy.Evaluator = nil
			}
			if mode == "canceled" {
				cancel()
			}
			got, err := policy.Decide(ctx, sampleInput())
			if (mode == "success") != got.Accepted {
				t.Fatalf("unexpected proposal %+v err=%v", got, err)
			}
			if (mode == "off" || mode == "canceled") && calls != 0 {
				t.Fatal("off/canceled made a request")
			}
			if mode == "unavailable" && (got.Tier != Complex || got.Reason != "unavailable_tier") {
				t.Fatal("unavailable tier was substituted")
			}
		})
	}
}

func TestPendingNeverWaitsAndDiscardsLateResult(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	policy := WorkPolicy{Evaluator: evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return certain(Simple), nil // even a misbehaving evaluator cannot publish a stale acceptance
	})}
	pending := policy.Start(t.Context(), sampleInput(), time.Second)
	defer pending.Close()
	defer close(release)
	<-started
	if _, ready := pending.Take(); ready {
		t.Fatal("unfinished proposal was ready")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("unused request was not canceled")
	}
	if _, ready := pending.Take(); ready {
		t.Fatal("late proposal was consumable")
	}
}

func TestCompletedDecisionSurvivesItsBudgetButNotParentCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		policy := WorkPolicy{Evaluator: evaluatorFunc(func(context.Context, Request) (Response, error) { return certain(Simple), nil })}
		pending := policy.Start(ctx, sampleInput(), 50*time.Millisecond)
		// Wait for completion without consuming it; the later admission boundary
		// is allowed to occur after the optional inference budget has elapsed.
		result := <-pending.done
		pending.done <- result
		time.Sleep(60 * time.Millisecond)
		if cancelParent {
			cancel()
		}
		got, ready := pending.Take()
		if ready == cancelParent || (ready && (!got.Proposal.Accepted || got.Err != nil)) {
			t.Fatalf("cancel=%v ready=%v result=%+v", cancelParent, ready, got)
		}
		if _, again := pending.Take(); again {
			t.Fatal("proposal consumed twice")
		}
		cancel()
		pending.Close()
	}
	if got := (WorkPolicy{}).Start(t.Context(), sampleInput(), time.Second); got != nil {
		t.Fatal("off started speculation")
	}
}
