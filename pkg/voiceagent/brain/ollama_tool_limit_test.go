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

// Exercise real tool execution and the Ollama transport without a model or
// external effects. Failed reads must reach a bounded, tools-free final turn.
func TestOllamaToolLimitFinalAnswer(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, mode := range []string{"answer", "empty", "filler", "thinking only", "tools again", "provider error", "truncated", "incomplete", "cancel"} {
			t.Run(map[bool]string{true: "stream", false: "full"}[streaming]+"/"+mode, func(t *testing.T) {
				calls, executed := 0, 0
				var final api.ChatRequest
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req api.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						return
					}
					calls++
					response := api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant"}}
					if calls <= maxToolIterations {
						if len(req.Tools) == 0 {
							t.Error("tools disabled before the round limit")
						}
						response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: mustArgs(map[string]any{"path": "missing.txt"})}}}
					} else {
						final = req
						if mode == "provider error" {
							http.Error(w, "unavailable", 500)
							return
						}
						switch mode {
						case "answer":
							response.Message.Content = "I could not read missing.txt because it does not exist. Here is a persona draft based on our conversation."
						case "filler":
							response.Message.Content = "Hmm."
						case "thinking only":
							response.Message.Thinking = "I need to answer."
						case "tools again":
							response.Message.Content = "Let me use another tool."
							response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file"}}}
						case "incomplete":
							response.Message.Content = "Not a complete answer"
							response.Done = false
						case "truncated":
							response.Message.Content = "I found"
							response.DoneReason = "length"
						}
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				host, _ := url.Parse(server.URL)
				o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 100, OllamaNumCtx: 32768}}
				opts := WorkerStreamOptions(true)
				opts.OnToolEnd = func(result ToolResult) {
					executed++
					if result.Err == "" {
						t.Error("fixture read should fail")
					}
					if mode == "cancel" && executed == maxToolIterations {
						cancel()
					}
				}
				var answer string
				var err error
				if streaming {
					var stream *Stream
					stream, err = o.ThinkStream(ctx, "Draft a persona from this conversation.", opts)
					if err == nil {
						answer, _ = drainStream(t, stream)
						err = (<-stream.Done).Err
					}
				} else {
					answer, err = o.ThinkFull(ctx, "Draft a persona from this conversation.", opts)
				}
				if mode == "answer" {
					if err != nil || !strings.Contains(answer, "Here is a persona draft") {
						t.Fatalf("answer=%q err=%v", answer, err)
					}
					history := o.History()
					if len(history) == 0 || history[len(history)-1].Content != answer {
						t.Fatalf("answer not recorded: %+v", history)
					}
				} else if err == nil {
					t.Fatalf("%s reported success: %q", mode, answer)
				}
				if mode != "answer" && answer != "" {
					t.Fatalf("failed final attempt leaked speech: %q", answer)
				}
				if strings.Contains(answer, "going in circles") {
					t.Fatalf("canned answer: %q", answer)
				}
				if executed != maxToolIterations {
					t.Fatalf("executed=%d", executed)
				}
				if mode == "cancel" {
					if !errors.Is(err, context.Canceled) || calls != maxToolIterations {
						t.Fatalf("limit bypass: calls=%d err=%v", calls, err)
					}
					return
				}
				wantCalls := maxToolIterations + 1
				if !streaming && mode == "truncated" {
					wantCalls += maxWorkerContinuations
				}
				if calls != wantCalls {
					t.Fatalf("requests=%d", calls)
				}
				foundFinalInstruction := false
				for _, m := range final.Messages {
					foundFinalInstruction = foundFinalInstruction || m.Content == toolLimitAnswerInstruction
				}
				if len(final.Tools) != 0 || !foundFinalInstruction {
					t.Fatalf("missing tools-free final instruction: %+v", final)
				}
				results := 0
				for _, m := range final.Messages {
					if m.Role == "tool" && strings.Contains(m.Content, "missing.txt") {
						results++
					}
				}
				if results != maxToolIterations {
					t.Fatalf("final answer lost tool evidence: %d results", results)
				}
			})
		}
	}
}
