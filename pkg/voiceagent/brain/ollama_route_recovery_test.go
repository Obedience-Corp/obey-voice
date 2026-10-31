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
			if _, err := decodeRouteRecovery(api.ChatResponse{Done: true, Message: api.Message{Content: raw}}, schema); err == nil {
				t.Fatal("accepted invalid decision")
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
		if _, err := decodeRouteRecovery(resp, schema); err == nil {
			t.Fatal("accepted truncated decision")
		}
		resp.DoneReason, resp.Done = "", false
		if _, err := decodeRouteRecovery(resp, schema); err == nil {
			t.Fatal("accepted incomplete decision")
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
