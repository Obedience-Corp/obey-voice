package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestWorkerBudgetFullHTTPToolLoopDecreasesPredict(t *testing.T) {
	var mu sync.Mutex
	var predicts []int
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options map[string]any `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		n++
		predicts = append(predicts, int(body.Options["num_predict"].(float64)))
		call := n
		mu.Unlock()
		if call == 1 {
			fmt.Fprint(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true,"prompt_eval_count":1,"eval_count":3}`)
			return
		}
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":2}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 2, MaxToolCalls: 1, MaxOutputTokens: 10})
	got, err := o.ThinkFull(b.Context(context.Background()), "hi", StreamOptions{ToolsEnabled: true, WorkerBudget: b})
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Fatalf("got %q", got)
	}
	if len(predicts) != 2 || predicts[0] != 10 || predicts[1] != 7 {
		t.Fatalf("num_predict = %v", predicts)
	}
}

func TestWorkerBudgetStreamRefusesThirdHTTPRequest(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"x"},"done":true,"done_reason":"stop","eval_count":1}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 2, MaxOutputTokens: 8})
	for i := 0; i < 2; i++ {
		s, err := o.ThinkStream(b.Context(context.Background()), "hi", StreamOptions{WorkerBudget: b})
		if err != nil {
			t.Fatal(err)
		}
		for range s.Chunks {
		}
		<-s.Done
	}
	if n != 2 {
		t.Fatalf("requests = %d", n)
	}
	if _, err := o.ThinkFull(b.Context(context.Background()), "third", StreamOptions{WorkerBudget: b}); err == nil {
		t.Fatal("third request admitted")
	}
}

func TestWorkerBudgetMissingEvalCountStopsBoundedContinuation(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			fmt.Fprint(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`)
			return
		}
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"should not run"},"done":true,"done_reason":"stop","eval_count":1}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 10}}
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 2, MaxOutputTokens: 4})
	_, _ = o.ThinkFull(b.Context(context.Background()), "hi", StreamOptions{ToolsEnabled: true, WorkerBudget: b})
	if requests != 1 {
		t.Fatalf("missing usage allowed continuation: %d requests", requests)
	}
}

func TestWorkerBudgetToolQuotaBlocksSecondFilesystemSideEffect(t *testing.T) {
	dir := t.TempDir()
	b := NewWorkerBudget(BudgetLimits{MaxToolCalls: 1})
	s := &toolSession{budget: b, boundary: NewBoundary("")}
	first := api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "first.txt", "content": "one"})}}
	second := api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "second.txt", "content": "two"})}}
	if got := s.execute(context.Background(), dir, first); got == "" {
		t.Fatal("empty first tool result")
	}
	if _, err := os.Stat(dir + "/first.txt"); err != nil {
		t.Fatalf("first side effect missing: %v", err)
	}
	if got := s.execute(context.Background(), dir, second); got == "" {
		t.Fatal("empty refusal")
	}
	if _, err := os.Stat(dir + "/second.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second side effect exists: %v", err)
	}
	if !errors.Is(s.limitErr, ErrWorkerBudgetExceeded) {
		t.Fatal("second tool was not refused")
	}
}

func TestStrictWorkerRefusesRunCommandWithoutSideEffect(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/hallucinated.txt"
	b := NewWorkerBudget(BudgetLimits{StrictTools: true, MaxToolCalls: 2})
	s := &toolSession{budget: b, boundary: NewBoundary("")}
	call := api.ToolCall{Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "touch " + marker})}}
	if got := s.execute(context.Background(), dir, call); got == "" {
		t.Fatal("empty refusal")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict refusal ran command: %v", err)
	}
	if !errors.Is(s.limitErr, ErrWorkerBudgetExceeded) {
		t.Fatal("strict refusal lacks budget error")
	}
}

func TestWorkerBudgetZeroRetriesDoesOneToolsRejectionRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"model does not support tools"}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m"}
	b := NewWorkerBudget(BudgetLimits{MaxRetries: 0})
	req := &api.ChatRequest{Tools: api.Tools{{Type: "function"}}, Options: map[string]any{}}
	if err := o.chat(b.Context(context.Background()), req, func(api.ChatResponse) error { return nil }); err == nil {
		t.Fatal("expected rejection")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}
