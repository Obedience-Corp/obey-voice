package brain

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

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func completionBudgetBrain(t *testing.T, handler http.HandlerFunc) (*OllamaBrain, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m", cfg: &config.Config{}, fullSystemPrompt: "system"}, calls
}

func TestWorkerBudgetCompleteCountsRequestAndOutput(t *testing.T) {
	o, calls := completionBudgetBrain(t, func(w http.ResponseWriter, r *http.Request) {
		var request api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Options["num_predict"] != float64(8) {
			t.Errorf("output ceiling: %v", request.Options)
		}
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"{\"answer\":\"ok\",\"updates\":[]}"},"done":true,"eval_count":2}`)
	})
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 1, MaxOutputTokens: 8})
	if _, err := o.Complete(b.Context(context.Background()), CompletionRequest{Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Complete(b.Context(context.Background()), CompletionRequest{Input: "x"}); !errors.Is(err, ErrWorkerBudgetExceeded) {
		t.Fatalf("second completion: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls=%d", calls.Load())
	}
}

func TestWorkerBudgetCompleteUnknownUsageStopsNextRequest(t *testing.T) {
	o, calls := completionBudgetBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"{\"answer\":\"ok\",\"updates\":[]}"},"done":true}`)
	})
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 2, MaxOutputTokens: 8})
	if _, err := o.Complete(b.Context(context.Background()), CompletionRequest{Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Complete(b.Context(context.Background()), CompletionRequest{Input: "x"}); err == nil {
		t.Fatal("unknown usage allowed next request")
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls=%d", calls.Load())
	}
}

func TestUnbudgetedCompleteRemainsCompatible(t *testing.T) {
	o, calls := completionBudgetBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"{\"answer\":\"ok\",\"updates\":[]}"},"done":true}`)
	})
	if _, err := o.Complete(context.Background(), CompletionRequest{Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls=%d", calls.Load())
	}
}
