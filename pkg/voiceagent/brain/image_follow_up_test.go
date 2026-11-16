package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// scriptedVision answers each chat request through respond, which sees the
// request and its sequence number and writes the whole response.
func scriptedVision(t *testing.T, respond func(w http.ResponseWriter, n int, req api.ChatRequest)) (*OllamaBrain, func() []api.ChatRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision", "tools"}})
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		mu.Unlock()
		respond(w, n, req)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	cfg := defaultConversationConfig()
	o := &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: cfg}
	sent := func() []api.ChatRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]api.ChatRequest(nil), requests...)
	}
	return o, sent
}

func answer(w http.ResponseWriter, req api.ChatRequest, msg api.Message, reason string) {
	_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: msg, Done: true, DoneReason: reason, Metrics: api.Metrics{EvalCount: 4}})
}

func routeChat(w http.ResponseWriter, req api.ChatRequest) {
	args := api.NewToolCallFunctionArguments()
	args.Set("kind", "chat")
	answer(w, req, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "route-1", Function: api.ToolCallFunction{Name: "route", Arguments: args}}}}, "stop")
}

func requestContaining(requests []api.ChatRequest, text string) bool {
	for _, req := range requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, text) {
				return true
			}
		}
	}
	return false
}

// A turn with large images is sent through every follow-up request it makes:
// a truncation continuation, a tool-call repair and the blocking path.
func TestLargeImageTurnSurvivesEveryFollowUpRequest(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 2, 4096, 4096)
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Attachments: refs}
	const complete = "Both images are plain grey squares of the same size."

	t.Run("truncation continuation", func(t *testing.T) {
		o, sent := scriptedVision(t, func(w http.ResponseWriter, n int, req api.ChatRequest) {
			switch {
			case hasTool(req.Tools, "route"):
				routeChat(w, req)
			case requestContaining([]api.ChatRequest{req}, "Continue the incomplete answer"):
				answer(w, req, api.Message{Role: "assistant", Content: complete}, "stop")
			case req.Stream != nil && !*req.Stream:
				answer(w, req, api.Message{Role: "assistant", Content: "Notes: two grey squares were attached."}, "stop")
			default:
				answer(w, req, api.Message{Role: "assistant", Content: "Both images are"}, "length")
			}
		})
		turn := runRoutedTurn(t, o, "Compare these.", opts)
		if turn.result.Err != nil {
			t.Fatalf("turn failed: %v", turn.result.Err)
		}
		if !requestContaining(sent(), "Continue the incomplete answer") {
			t.Fatal("no continuation was sent")
		}
	})

	t.Run("tool parse repair", func(t *testing.T) {
		o, sent := scriptedVision(t, func(w http.ResponseWriter, n int, req api.ChatRequest) {
			switch {
			case n == 1:
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": malformedToolFixture})
			case hasTool(req.Tools, "route"):
				routeChat(w, req)
			default:
				answer(w, req, api.Message{Role: "assistant", Content: complete}, "stop")
			}
		})
		turn := runRoutedTurn(t, o, "Compare these.", opts)
		if turn.result.Err != nil {
			t.Fatalf("turn failed: %v", turn.result.Err)
		}
		if !requestContaining(sent(), toolParseRetryInstruction) {
			t.Fatal("no repair was sent")
		}
	})

	t.Run("blocking request", func(t *testing.T) {
		o, sent := scriptedVision(t, func(w http.ResponseWriter, n int, req api.ChatRequest) {
			if hasTool(req.Tools, "route") {
				routeChat(w, req)
				return
			}
			answer(w, req, api.Message{Role: "assistant", Content: complete}, "stop")
		})
		if _, err := o.ThinkFull(t.Context(), "Compare these.", opts); err != nil {
			t.Fatalf("turn failed: %v", err)
		}
		got := sent()
		if len(got) != 2 {
			t.Fatalf("sent %d requests", len(got))
		}
		if _, images := requestShape(got[1]); images != len(refs) {
			t.Fatalf("reply carried %d images", images)
		}
	})
}
