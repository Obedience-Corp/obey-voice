package brain

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestRouteRecoveryExplainsRejectedResponsesWithoutLeakingPrivateOutput(t *testing.T) {
	const privateThinking = "private internal reasoning"
	const privateReply = `{"kind":"chat"}`
	for _, tc := range []struct {
		name, want string
		change     func(*api.ChatResponse)
	}{
		{"unfinished", "ended without completion", func(r *api.ChatResponse) { r.Done = false }},
		{"budget", "exhausted its output budget", func(r *api.ChatResponse) {
			r.DoneReason = "length"
			r.Message.Content = `{"kind":"chat"`
		}},
		{"tools", "tool calls instead of a JSON decision", func(r *api.ChatResponse) {
			r.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "private_tool"}}}
		}},
		{"oversized", "exceeded the response size limit", func(r *api.ChatResponse) {
			r.Message.Content = strings.Repeat("x", 4097)
		}},
		{"thinking only", "reasoning without a decision", func(r *api.ChatResponse) { r.Message.Content = "" }},
		{"empty", "contained no decision", func(r *api.ChatResponse) {
			r.Message.Content, r.Message.Thinking = "", ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := api.ChatResponse{Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 256},
				Message: api.Message{Role: "assistant", Content: privateReply, Thinking: privateThinking}}
			tc.change(&response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(base, server.Client()), model: "fixture",
				cfg: &config.Config{PromptsDir: t.TempDir()}}
			callbacks := 0
			err := o.chatRouteRecovery(t.Context(), o.newChatRequest(nil, liveRouterTools()), func(r api.ChatResponse) error {
				callbacks++
				if r.Message.Content != "" || r.Message.Thinking != "" || len(r.Message.ToolCalls) != 0 {
					t.Fatal("rejected private output reached the response callback")
				}
				if r.EvalCount != response.EvalCount {
					t.Fatal("usage metadata was lost")
				}
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || callbacks != 1 {
				t.Fatalf("callbacks=%d err=%v, want %s", callbacks, err, tc.want)
			}
			for _, want := range []string{
				fmt.Sprintf("done=%t", response.Done), fmt.Sprintf("reason=%q", response.DoneReason),
				"generated_tokens=256", fmt.Sprintf("reply_bytes=%d", len(response.Message.Content)),
				fmt.Sprintf("json_complete=%t", json.Valid([]byte(response.Message.Content))),
				fmt.Sprintf("thinking_bytes=%d", len(response.Message.Thinking)),
				fmt.Sprintf("tool_calls=%d", len(response.Message.ToolCalls)),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %s", err, want)
				}
			}
			for _, private := range []string{privateThinking, privateReply, "private_tool"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("diagnostic exposed private model output")
				}
			}
		})
	}
}

func TestRouteRecoveryKeepsThinkingPrivateForValidDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop",
			Message: api.Message{Role: "assistant", Thinking: "private reasoning", Content: `{"kind":"chat"}`}})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(base, server.Client()), model: "fixture",
		cfg: &config.Config{PromptsDir: t.TempDir()}}
	err := o.chatRouteRecovery(t.Context(), o.newChatRequest(nil, liveRouterTools()), func(r api.ChatResponse) error {
		if r.Message.Content != "" || r.Message.Thinking != "" || len(r.Message.ToolCalls) != 1 {
			t.Fatal("expected only the validated route, with no private content or reasoning")
		}
		if toolCallArg(r.Message.ToolCalls[0], "kind") != "chat" {
			t.Fatal("wrong route")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
