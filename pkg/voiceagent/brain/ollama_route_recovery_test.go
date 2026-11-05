package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestMissingRouteRecoversConversation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "streaming"}[streaming], func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requests++
				msg := api.Message{Role: "assistant"}
				switch requests {
				case 1:
					msg.Content = "Discard this undecided reply."
				case 2:
					if len(req.Tools) != 0 || len(req.Format) == 0 || req.Stream == nil || *req.Stream || thinkEnabled(req.Think) {
						t.Errorf("recovery must use private schema JSON: %+v", req)
					}
					for _, m := range req.Messages {
						if strings.Contains(m.Content, "Discard this") {
							t.Error("undecided response leaked into recovery context")
						}
					}
					msg.Content = `{"kind":"chat"}`
				case 3:
					if len(req.Tools) != 0 || len(req.Format) != 0 {
						t.Error("answer retained routing schema/tools")
					}
					msg.Content = "Fair enough, Lance."
				default:
					t.Error("unbounded recovery")
					http.Error(w, "too many requests", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true})
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(base, server.Client()), model: "fixture", cfg: &config.Config{MaxHistory: 20, PromptsDir: t.TempDir()}}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			var answer string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Nothing's wrong with me. You're the one with that name.", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				answer, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				answer, err = o.ThinkFull(t.Context(), "Nothing's wrong with me. You're the one with that name.", opts)
			}
			if err != nil || answer != "Fair enough, Lance." || requests != 3 {
				t.Fatalf("answer=%q requests=%d err=%v", answer, requests, err)
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, "Discard this") || strings.Contains(turn.Content, `{"kind":"chat"}`) {
					t.Fatalf("private output leaked: %+v", turn)
				}
			}
		})
	}
}

func TestRouteRecoveryRejectsInvalidDecisions(t *testing.T) {
	schema := liveRouterTools(&WorkerTierAvailability{Simple: true})[0].Function.Parameters
	for _, raw := range []string{
		`{}`, `null`, `{"kind":"unknown","tier":"simple"}`, `{"kind":"chat"}`,
		`{"kind":null,"tier":"simple"}`, `{"kind":"chat","tier":3}`,
		`{"kind":"spawn","tier":"bogus"}`, `{"kind":"chat","tier":"simple","answer":"hello"}`,
		`{"kind":"chat","tier":"simple"} trailing`, `I will do that.`,
	} {
		t.Run(raw, func(t *testing.T) {
			for _, reason := range []string{"stop", "length"} {
				if _, err := decodeRouteRecovery(api.ChatResponse{Done: true, DoneReason: reason, Message: api.Message{Content: raw}}, schema); err == nil {
					t.Fatalf("accepted invalid decision with reason=%s", reason)
				}
			}
		})
	}
	for _, kind := range []string{"chat", "spawn", "narrate"} {
		resp := api.ChatResponse{Done: true, Message: api.Message{Content: `{"kind":"` + kind + `","tier":"simple","reason_code":"bounded_action"}`}}
		args, err := decodeRouteRecovery(resp, schema)
		if err != nil || args.ToMap()["kind"] != kind {
			t.Fatalf("kind=%s args=%v err=%v", kind, args, err)
		}
		resp.DoneReason = "length"
		if _, err := decodeRouteRecovery(resp, schema); err != nil {
			t.Fatalf("rejected complete decision at token limit: %v", err)
		}
		resp.Message.Content = strings.TrimSuffix(resp.Message.Content, "}")
		if _, err := decodeRouteRecovery(resp, schema); err == nil {
			t.Fatal("accepted incomplete JSON at token limit")
		}
		resp.DoneReason, resp.Done = "", false
		if _, err := decodeRouteRecovery(resp, schema); err == nil {
			t.Fatal("accepted incomplete decision")
		}
	}
}

func TestRouteRecoveryCompleteDecisionRetainsOutputAccounting(t *testing.T) {
	for _, generated := range []int{256, 257} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Options["num_predict"] != float64(256) {
				t.Errorf("unexpected generation budget: %v", request.Options["num_predict"])
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "length",
				Metrics: api.Metrics{EvalCount: generated}, Message: api.Message{Content: `{"kind":"spawn"}`}})
		}))
		defer server.Close()
		host, _ := url.Parse(server.URL)
		o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", cfg: &config.Config{PromptsDir: t.TempDir()}}
		budget := NewWorkerBudget(BudgetLimits{MaxRequests: 1, MaxRetries: 1, MaxOutputTokens: 256})
		callbacks := 0
		err := o.chatRouteRecovery(budget.Context(t.Context()), o.newChatRequest(nil, liveRouterTools()), func(response api.ChatResponse) error {
			callbacks++
			if response.EvalCount != generated || response.DoneReason != "stop" || len(response.Message.ToolCalls) != 1 {
				t.Fatalf("validated decision lost usage or completion: %+v", response)
			}
			return nil
		})
		if generated == 256 {
			if err != nil || callbacks != 1 {
				t.Fatalf("valid decision rejected: callbacks=%d err=%v", callbacks, err)
			}
		} else if !errors.Is(err, ErrWorkerBudgetExceeded) || callbacks != 0 {
			t.Fatalf("complete JSON bypassed output budget: callbacks=%d err=%v", callbacks, err)
		}
		if usage := budget.Snapshot(); usage.OutputTokens != generated || usage.Requests != 1 || usage.Retries != 1 {
			t.Fatalf("lost routing usage: %+v", usage)
		}
	}
}

func TestRouteRecoveryHonorsCancellationAndBudget(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{PromptsDir: t.TempDir()}}
	req := o.newChatRequest([]api.Message{{Role: "user", Content: "hello"}}, liveRouterTools())
	for _, mode := range []string{"cancel", "retry budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := context.Canceled
			if mode == "cancel" {
				cancel()
			} else {
				ctx = NewWorkerBudget(BudgetLimits{MaxRequests: 2, MaxRetries: 0}).Context(ctx)
				want = ErrWorkerBudgetExceeded
			}
			err := o.chatRouteRecovery(ctx, req, func(api.ChatResponse) error { t.Fatal("unexpected response"); return nil })
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if len(req.Tools) == 0 || len(req.Format) != 0 {
				t.Fatal("mutated original request")
			}
		})
	}
}

func TestRouteRecoveryAcceptsCompleteDecisionAtTokenLimit(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "streaming"}[streaming], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				calls++
				response := api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant"}}
				switch calls {
				case 1:
					response.Message.Content = "Undecided private reply."
				case 2:
					if request.Options["num_predict"] != float64(256) {
						t.Errorf("recovery cap changed: %v", request.Options["num_predict"])
					}
					response.Message.Content = `{"kind":"chat"}` + strings.Repeat(" ", 25)
					response.DoneReason = "length"
					response.EvalCount = 256
				case 3:
					if len(request.Tools) > 0 || len(request.Format) > 0 {
						t.Error("validated chat route retained routing tools or schema")
					}
					response.Message.Content = "The garden is ready."
				default:
					t.Error("route recovery retried without bound")
					http.Error(w, "unexpected retry", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", cfg: &config.Config{MaxHistory: 20, PromptsDir: t.TempDir()}}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			var answer string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "How is the garden?", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				answer, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				answer, err = o.ThinkFull(t.Context(), "How is the garden?", opts)
			}
			if err != nil || answer != "The garden is ready." || calls != 3 {
				t.Fatalf("complete decision was rejected: answer=%q calls=%d err=%v", answer, calls, err)
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, "Undecided private reply") || strings.Contains(turn.Content, `{"kind":"chat"}`) {
					t.Fatal("private routing content entered reply history")
				}
			}
		})
	}
}
