package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func exampleRequest() brain.AddressRequest {
	return brain.AddressRequest{Participants: []brain.GroupParticipant{{Name: "Jarvis"}, {Name: "Santa"}}, Account: "user", Text: "Jarvis, explain this"}
}

func exampleResult(choice string) systemOneResponse {
	p := 0.0
	probs := map[string]float64{"person_0": 0, "person_1": 0, "none": 0, "ambiguous": 0, "unavailable": 0}
	probs[choice] = 1
	return systemOneResponse{Model: "test", Answers: map[string]answer{"target": {Choice: choice, Confidence: 1, Probabilities: probs}, "bare": {Noul: &p}}}
}

func TestOptionalFallbackBoundaries(t *testing.T) {
	for _, kind := range []string{"off", "success", "unavailable", "ambiguous", "uncertain", "invalid-json", "missing-answer", "bad-probabilities", "http-error", "timeout", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if kind == "timeout" {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				if kind == "http-error" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if kind == "invalid-json" {
					_, _ = w.Write([]byte("not json"))
					return
				}
				result := exampleResult("person_0")
				if kind == "unavailable" || kind == "ambiguous" {
					result = exampleResult(kind)
				}
				if kind == "missing-answer" {
					delete(result.Answers, "bare")
				}
				if kind == "bad-probabilities" {
					a := result.Answers["target"]
					a.Probabilities["none"] = 2
					result.Answers["target"] = a
				}
				if kind == "uncertain" {
					a := result.Answers["target"]
					a.Confidence = 0.5
					result.Answers["target"] = a
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			defer close(release)
			c := &candidate{endpoint: server.URL, client: server.Client(), budget: 30 * time.Millisecond}
			if kind == "off" {
				c = nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			calls := 0
			expected := brain.Addressee{Kind: "none"}
			// This fallback deliberately has no Ollama client or dependency.
			fallback := func(ctx context.Context, req brain.AddressRequest) (brain.Addressee, error) {
				calls++
				if ctx.Err() != nil {
					t.Fatal("fallback received canceled parent")
				}
				return expected, nil
			}
			got, used, _, reason, err := optionalResolve(ctx, c, fallback, exampleRequest())
			switch kind {
			case "cancelled":
				if !errors.Is(err, context.Canceled) || calls != 0 || requests.Load() != 0 {
					t.Fatalf("canceled: %v calls=%d requests=%d", err, calls, requests.Load())
				}
			case "success":
				if err != nil || calls != 0 || used || got.Name != "Jarvis" {
					t.Fatalf("success: %+v %v", got, err)
				}
			default:
				if err != nil || calls != 1 || got != expected || reason == "" {
					t.Fatalf("fallback: %+v err=%v calls=%d reason=%s", got, err, calls, reason)
				}
				if kind == "off" && requests.Load() != 0 {
					t.Fatal("off made optional request")
				}
				if kind != "off" && !used {
					t.Fatal("missing fallback flag")
				}
			}
		})
	}
}

func TestFallbackErrorIsPreserved(t *testing.T) {
	want := errors.New("original admission rejected target")
	_, _, _, _, err := optionalResolve(t.Context(), nil, func(context.Context, brain.AddressRequest) (brain.Addressee, error) { return brain.Addressee{}, want }, exampleRequest())
	if !errors.Is(err, want) {
		t.Fatalf("lost admission error: %v", err)
	}
}

func TestCancelInFlightDoesNotFallback(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { <-started; cancel() }()
	c := &candidate{endpoint: server.URL, client: server.Client(), budget: time.Second}
	called := false
	_, _, _, _, err := optionalResolve(ctx, c, func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		called = true
		return brain.Addressee{}, nil
	}, exampleRequest())
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("error=%v fallback=%v", err, called)
	}
}

func TestAgentBareAndDuplicateTargets(t *testing.T) {
	req := exampleRequest()
	req.Account = "agent"
	r := exampleResult("person_0")
	yes := 1.0
	r.Answers["bare"] = answer{Noul: &yes}
	got, accepted, err := interpret(req, r)
	if err != nil || !accepted || got.Bare {
		t.Fatalf("agent bare: %+v %v", got, err)
	}
	req.Participants[1].Aliases = []string{"Jarvis"}
	_, accepted, err = interpret(req, r)
	if err != nil || accepted {
		t.Fatal("colliding target accepted")
	}
}

func TestScoringUsesHostRosterRejection(t *testing.T) {
	req := exampleRequest()
	req.Participants = []brain.GroupParticipant{{Name: "Santa North"}, {Name: "Santa South"}}
	if !equivalent(brain.Addressee{Kind: "address", Name: "Santa"}, brain.Addressee{Kind: "ambiguous"}, req) {
		t.Fatal("shared name must score as host rejection")
	}
	if !equivalent(brain.Addressee{Kind: "address", Name: "Charles"}, brain.Addressee{Kind: "unavailable"}, req) {
		t.Fatal("absent name must score as host rejection")
	}
	if equivalent(brain.Addressee{Kind: "address", Name: "Santa North"}, brain.Addressee{Kind: "ambiguous"}, req) {
		t.Fatal("arbitrary unique selection must not score as rejection")
	}
}

func TestRequestUsesOnlyFixtureState(t *testing.T) {
	req := exampleRequest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State brain.AddressRequest `json:"state"`
			Model string               `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body.State, req) || body.Model != "test-model" {
			t.Errorf("unexpected request %+v", body)
		}
		_ = json.NewEncoder(w).Encode(exampleResult("person_0"))
	}))
	defer server.Close()
	c := candidate{endpoint: server.URL, model: "test-model", client: server.Client(), budget: time.Second}
	if _, ok, _, err := c.decide(t.Context(), req); err != nil || !ok {
		t.Fatalf("decide: %v", err)
	}
}
