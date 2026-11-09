package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

const workerExitFixture = "mlx runner exited unexpectedly: signal: broken pipe"

func outageTestBrain(t *testing.T, handler http.HandlerFunc) *OllamaBrain {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	return &OllamaBrain{client: api.NewClient(u, server.Client()), model: "fixture", workDir: t.TempDir(),
		cfg: &config.Config{OllamaHost: server.URL, MaxHistory: 20, OllamaNumCtx: 8192, PromptsDir: t.TempDir()}}
}

func TestOllamaOutageRetryBoundaries(t *testing.T) {
	for _, mode := range []string{"HTTP recovery", "NDJSON recovery", "exhausted", "text", "thinking", "tool", "done", "callback", "cancel", "request budget", "retry budget", "token budget", "authentication", "input", "arbitrary 500", "transient summary"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			o := outageTestBrain(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 2 && (mode == "HTTP recovery" || mode == "NDJSON recovery") {
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Content: "OK"}})
					return
				}
				response := api.ChatResponse{}
				switch mode {
				case "text":
					response.Message.Content = "Partial"
				case "thinking":
					response.Message.Thinking = "Reasoning"
				case "tool":
					response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file"}}}
				case "done":
					response.Done = true
				case "cancel":
					cancel()
				case "callback":
					response.Message.Content = "answer"
				}
				partial := response.Message.Content != "" || response.Message.Thinking != "" || len(response.Message.ToolCalls) > 0 || response.Done
				if partial {
					_ = json.NewEncoder(w).Encode(response)
				} else if mode != "NDJSON recovery" {
					status := 500
					if mode == "authentication" {
						status = 401
					}
					if mode == "input" {
						status = 400
					}
					w.WriteHeader(status)
				}
				message := workerExitFixture
				if mode == "arbitrary 500" {
					message = "cannot parse generation"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			})
			var states []string
			o.SetContinuitySink(func(e events.Event) {
				if info, ok := e.(events.Info); ok && info.ProviderStatus != nil {
					states = append(states, info.ProviderStatus.State)
				}
			})
			var budget *WorkerBudget
			switch mode {
			case "request budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 1, MaxRetries: 1})
			case "retry budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5})
			case "token budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5, MaxRetries: 1, MaxOutputTokens: 20})
			}
			ctx = budget.Context(ctx)
			if mode == "transient summary" {
				ctx = context.WithValue(ctx, disableOutageRetryKey{}, true)
			}
			err := o.chat(ctx, o.newChatRequest([]api.Message{{Role: "user", Content: "Hello"}}, nil), func(api.ChatResponse) error {
				if mode == "callback" {
					return errors.New(workerExitFixture)
				}
				return nil
			})
			want := int32(1)
			if mode == "HTTP recovery" || mode == "NDJSON recovery" || mode == "exhausted" {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d err=%v", calls.Load(), want, err)
			}
			if mode == "HTTP recovery" || mode == "NDJSON recovery" {
				if err != nil || !reflect.DeepEqual(states, []string{"retrying", "available"}) {
					t.Fatalf("err=%v states=%v", err, states)
				}
			} else if err == nil {
				t.Fatal("failure was hidden")
			}
			if mode == "exhausted" && !reflect.DeepEqual(states, []string{"retrying", "unavailable"}) {
				t.Fatalf("states=%v", states)
			}
			if budget != nil && budget.Snapshot().Requests != int(want) {
				t.Fatalf("budget=%+v", budget.Snapshot())
			}
			if budget != nil && !errors.Is(err, ErrWorkerBudgetExceeded) {
				t.Fatalf("budget error lost: %v", err)
			}
		})
	}
}

func TestOllamaOutageCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	o := outageTestBrain(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, workerExitFixture, 500) })
	o.SetContinuitySink(func(e events.Event) {
		if info, ok := e.(events.Info); ok && info.ProviderStatus != nil && info.ProviderStatus.State == "retrying" {
			cancel()
		}
	})
	err := o.chat(ctx, o.newChatRequest(nil, nil), func(api.ChatResponse) error { return nil })
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}
