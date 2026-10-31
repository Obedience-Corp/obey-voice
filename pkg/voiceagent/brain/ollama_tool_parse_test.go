package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

const malformedToolFixture = "XML syntax error on line 25: unexpected EOF"

func TestOllamaMalformedToolRecoveryExecutesOnce(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, ndjson := range []bool{false, true} {
			t.Run(map[bool]string{false: "full", true: "stream"}[streaming]+map[bool]string{false: "/HTTP", true: "/NDJSON"}[ndjson], func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "books.txt"), []byte("The Wind in the Willows"), 0600); err != nil {
					t.Fatal(err)
				}
				var requests []api.ChatRequest
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req api.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						return
					}
					requests = append(requests, req)
					switch len(requests) {
					case 1:
						if !ndjson {
							w.WriteHeader(500)
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"error": malformedToolFixture})
					case 2:
						if len(req.Tools) == 0 || req.Messages[len(req.Messages)-1].Content != toolParseRetryInstruction {
							t.Error("retry lost tools or correction")
						}
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: mustArgs(map[string]any{"path": "books.txt"})}}}}})
					case 3:
						found := false
						for _, m := range req.Messages {
							if m.Role == "tool" && strings.Contains(m.Content, "The Wind in the Willows") {
								found = true
							}
						}
						if !found {
							t.Error("answer request lacks library evidence")
						}
						_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Role: "assistant", Content: "Your library includes The Wind in the Willows."}})
					default:
						t.Error("unbounded retry")
						http.Error(w, "extra request", 500)
					}
				}))
				defer server.Close()
				host, _ := url.Parse(server.URL)
				o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: dir, cfg: &config.Config{MaxHistory: 20, OllamaNumCtx: 8192}}
				executed := 0
				opts := WorkerStreamOptions(true)
				opts.OnToolStart = func(ToolCall) { executed++ }
				var answer string
				var err error
				if streaming {
					var stream *Stream
					stream, err = o.ThinkStream(t.Context(), "Which books can I narrate?", opts)
					if err == nil {
						answer, _ = drainStream(t, stream)
						err = (<-stream.Done).Err
					}
				} else {
					answer, err = o.ThinkFull(t.Context(), "Which books can I narrate?", opts)
				}
				if err != nil || answer != "Your library includes The Wind in the Willows." || executed != 1 || len(requests) != 3 {
					t.Fatalf("answer=%q err=%v executed=%d requests=%d", answer, err, executed, len(requests))
				}
				for _, turn := range o.History() {
					if strings.Contains(turn.Content, toolParseRetryInstruction) {
						t.Fatal("repair instruction entered conversation history")
					}
				}
			})
		}
	}
}

func TestOllamaMalformedToolRetryBoundaries(t *testing.T) {
	for _, mode := range []string{"repeated", "text", "thinking", "tool", "done", "callback", "no tools", "other error", "bad request", "cancel", "request budget", "retry budget", "token budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			requests := 0
			delivered := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch mode {
				case "text", "thinking", "tool", "done", "callback":
					resp := api.ChatResponse{}
					switch mode {
					case "text":
						resp.Message.Content = "Partial answer."
					case "thinking":
						resp.Message.Thinking = "Thinking."
					case "tool":
						resp.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file"}}}
					case "done":
						resp.Done = true
					}
					_ = json.NewEncoder(w).Encode(resp)
				case "cancel":
					cancel()
				case "bad request":
					w.WriteHeader(400)
				default:
					w.WriteHeader(500)
				}
				message := malformedToolFixture
				if mode == "other error" {
					message = "model runner crashed"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			}))
			defer server.Close()
			host, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(host, server.Client()), cfg: &config.Config{OllamaNumCtx: 8192}}
			notices := 0
			o.SetInfoSink(func(events.Info) { notices++ })
			req := o.newChatRequest([]api.Message{{Role: "user", Content: "inspect the library"}}, voiceAssistantTools(nil))
			if mode == "no tools" {
				req.Tools = nil
			}
			var budget *WorkerBudget
			switch mode {
			case "request budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 1, MaxRetries: 1})
			case "retry budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5, MaxRetries: 0})
			case "token budget":
				budget = NewWorkerBudget(BudgetLimits{MaxRequests: 5, MaxRetries: 1, MaxOutputTokens: 100})
			}
			ctx = budget.Context(ctx)
			err := o.chat(ctx, req, func(api.ChatResponse) error {
				delivered++
				if mode == "callback" {
					return errors.New(malformedToolFixture)
				}
				return nil
			})
			wantRequests := 1
			if mode == "repeated" {
				wantRequests = 2
			}
			if err == nil || requests != wantRequests {
				t.Fatalf("requests=%d want=%d delivered=%d err=%v", requests, wantRequests, delivered, err)
			}
			if notices != wantRequests-1 {
				t.Fatalf("retry notices=%d, actual retries=%d", notices, wantRequests-1)
			}
			if budget != nil && budget.Snapshot().Requests != wantRequests {
				t.Fatalf("request accounting=%+v", budget.Snapshot())
			}
			if budget != nil && !errors.Is(err, ErrWorkerBudgetExceeded) {
				t.Fatalf("budget not enforced: %v", err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if len(req.Messages) != 1 {
				t.Fatal("retry mutated caller messages")
			}
		})
	}
}

func TestModelMalformedToolCall(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{malformedToolFixture, true},
		{"XML syntax error on line 12: element <function> closed by </parameter>", true},
		{"unexpected EOF", false},
		{"model runner crashed", false},
		{"XML syntax error on line 2: invalid character entity", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			if got := modelMalformedToolCall(errors.New(tc.message)); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if got := modelMalformedToolCall(api.StatusError{StatusCode: 500, ErrorMessage: tc.message}); got != tc.want {
				t.Fatalf("HTTP 500 got %v, want %v", got, tc.want)
			}
			if modelMalformedToolCall(api.StatusError{StatusCode: 400, ErrorMessage: tc.message}) {
				t.Fatal("retried HTTP 400")
			}
		})
	}
	if modelMalformedToolCall(nil) {
		t.Fatal("nil error classified as malformed")
	}
}
