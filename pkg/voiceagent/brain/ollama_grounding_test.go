package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestLiveRoutingDoesNotSpeakUnexecutedPromises(t *testing.T) {
	for _, recoverRoute := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing route", true: "retry routes work"}[recoverRoute], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if calls == 2 && !strings.Contains(req.Messages[len(req.Messages)-1].Content, missingRouteInstruction) {
					t.Error("retry lost routing instruction")
				}
				msg := api.Message{Role: "assistant", Content: "I searched and attached a dog image."}
				if recoverRoute && calls == 2 {
					msg.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn"})}}}
				}
				if err := json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(endpoint, http.DefaultClient), model: "fixture", cfg: &config.Config{AgentName: "Obey", MaxHistory: 10}}
			stream, err := o.ThinkStream(t.Context(), "Find and attach a PNG of a dog", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
			if err != nil {
				t.Fatal(err)
			}
			spoken, _ := drainStream(t, stream)
			result := <-stream.Done
			if spoken != "" || calls != 2 {
				t.Fatalf("spoken=%q requests=%d", spoken, calls)
			}
			if recoverRoute {
				if result.Err != nil || result.Route != "spawn" {
					t.Fatalf("%+v", result)
				}
			} else if result.Err == nil {
				t.Fatal("missing route accepted")
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, "I searched") {
					t.Fatal("unexecuted promise entered history")
				}
			}
		})
	}
}

func TestBlockingRoutingPreservesChatAnswer(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "fixture", cfg: &config.Config{MaxHistory: 10}}
	// This fixture follows route(chat); its answer must remain available.
	answer, err := o.ThinkFull(t.Context(), "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil || answer != "hi" || count != 2 {
		t.Fatalf("answer=%q calls=%d err=%v", answer, count, err)
	}
}
