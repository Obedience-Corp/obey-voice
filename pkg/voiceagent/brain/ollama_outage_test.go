package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
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
	for _, mode := range []string{"HTTP recovery", "NDJSON recovery", "exhausted", "text", "thinking", "tool", "done", "callback", "cancel", "request budget", "retry budget", "token budget", "authentication", "input", "arbitrary 500", "transient summary", "truncated HTTP recovery", "truncated exhausted", "truncated text", "truncated thinking", "truncated tool", "truncated done", "truncated callback"} {
		t.Run(mode, func(t *testing.T) {
			kind := strings.TrimPrefix(mode, "truncated ")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			o := outageTestBrain(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 2 && (kind == "HTTP recovery" || kind == "NDJSON recovery") {
					_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Content: "OK"}})
					return
				}
				response := api.ChatResponse{}
				switch kind {
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
				if strings.HasPrefix(mode, "truncated ") {
					// A short 200 body exercises the pinned client's transport
					// error, not a synthetic callback error or NDJSON error frame.
					w.Header().Set("Content-Length", "10000")
					w.WriteHeader(http.StatusOK)
					if partial {
						_ = json.NewEncoder(w).Encode(response)
					}
					w.(http.Flusher).Flush()
					return
				}
				if partial {
					_ = json.NewEncoder(w).Encode(response)
				} else if kind != "NDJSON recovery" {
					status := 500
					if kind == "authentication" {
						status = 401
					}
					if kind == "input" {
						status = 400
					}
					w.WriteHeader(status)
				}
				message := workerExitFixture
				if kind == "arbitrary 500" {
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
			switch kind {
			case "request budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 1, MaxRetries: 1})
			case "retry budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5})
			case "token budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5, MaxRetries: 1, MaxOutputTokens: 20})
			}
			ctx = budget.Context(ctx)
			if kind == "transient summary" {
				ctx = context.WithValue(ctx, disableOutageRetryKey{}, true)
			}
			err := o.chat(ctx, o.newChatRequest([]api.Message{{Role: "user", Content: "Hello"}}, nil), func(api.ChatResponse) error {
				if mode == "truncated callback" {
					return fmt.Errorf("callback: %w", io.ErrUnexpectedEOF)
				}
				if kind == "callback" {
					return errors.New(workerExitFixture)
				}
				return nil
			})
			want := int32(1)
			if kind == "HTTP recovery" || kind == "NDJSON recovery" || kind == "exhausted" {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d err=%v", calls.Load(), want, err)
			}
			if kind == "HTTP recovery" || kind == "NDJSON recovery" {
				if err != nil || !reflect.DeepEqual(states, []string{"retrying", "available"}) {
					t.Fatalf("err=%v states=%v", err, states)
				}
			} else if err == nil {
				t.Fatal("failure was hidden")
			}
			if kind == "exhausted" && !reflect.DeepEqual(states, []string{"retrying", "unavailable"}) {
				t.Fatalf("states=%v", states)
			}
			if strings.HasPrefix(mode, "truncated ") && kind != "HTTP recovery" {
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("truncation identity lost: %v", err)
				}
				var outage *ProviderOutage
				if kind == "callback" {
					if errors.As(err, &outage) || len(states) != 0 {
						t.Fatalf("callback failure classified as outage: %v, %v", err, states)
					}
				} else if !errors.As(err, &outage) || outage.Delivered != (kind != "exhausted") {
					t.Fatalf("outage delivery boundary lost: %+v", outage)
				}
				if kind != "callback" && kind != "exhausted" && !reflect.DeepEqual(states, []string{"unavailable"}) {
					t.Fatalf("transport outage warning lost: %v", states)
				}
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
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			o := outageTestBrain(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if truncated {
					w.Header().Set("Content-Length", "100")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					return
				}
				http.Error(w, workerExitFixture, 500)
			})
			o.SetContinuitySink(func(e events.Event) {
				if info, ok := e.(events.Info); ok && info.ProviderStatus != nil && info.ProviderStatus.State == "retrying" {
					cancel()
				}
			})
			err := o.chat(ctx, o.newChatRequest(nil, nil), func(api.ChatResponse) error { return nil })
			if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}
