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

// A completed own reply is context, not a prefix to the next response. Check
// both an agent handoff and the human follow-up, through the real wire path.
func TestGroupTurnsSeparateHistoryFromNewResponse(t *testing.T) {
	prior := "The garden has blue flowers, two apple trees and a pond. We can add a bench by the pond and lavender along the path."
	replies := []string{"Jarvis, the bench can face the pond.", "The path can stay clear."}
	var requests []api.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		index := len(requests)
		requests = append(requests, req)
		if index >= len(replies) {
			t.Error("unexpected extra model request")
			http.Error(w, "extra request", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: replies[index]}, Done: true, DoneReason: "stop"})
	}))
	defer server.Close()
	host, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", cfg: &config.Config{ActivePersona: "veronica", AgentName: "Veronica", MaxHistory: 20, OllamaNumCtx: 8192}}
	o.SetGroupParticipants([]GroupParticipant{{Name: "Veronica"}, {Name: "Jarvis"}})
	history := []Turn{
		{Role: "user", Content: "Describe the garden."},
		{Role: "assistant", Content: prior, PersonaID: "veronica", PersonaDisplayName: "Veronica", ResponseID: "earlier"},
		{Role: "user", Content: "Jarvis, please discuss the bench with Veronica."},
		{Role: "assistant", Content: "Veronica, which way should the bench face?", PersonaID: "jarvis", PersonaDisplayName: "Jarvis", ResponseID: "handoff"},
	}
	if err := o.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	for i, input := range []string{"unused handoff input", "Keep the path clear."} {
		stream, err := o.ThinkStream(t.Context(), input, StreamOptions{AgentContinuation: i == 0, Identity: &TurnIdentity{PersonaID: "veronica", PersonaDisplayName: "Veronica", ResponseID: []string{"new-handoff", "new-user"}[i]}})
		if err != nil {
			t.Fatal(err)
		}
		spoken, _ := drainStream(t, stream)
		result := <-stream.Done
		if result.Err != nil || spoken != replies[i] {
			t.Fatalf("turn %d spoken=%q result=%+v", i, spoken, result)
		}
		last := o.History()[len(o.History())-1]
		if last.Content != replies[i] {
			t.Fatalf("stored response includes old text: %+v", last)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, req := range requests {
		copies := 0
		for _, message := range req.Messages {
			copies += strings.Count(message.Content, prior)
		}
		if copies != 1 {
			t.Fatalf("request %d repeats earlier answer %d times", i, copies)
		}
	}
	first := requests[0].Messages
	if last := first[len(first)-1]; last.Role != "user" || last.Content != "Jarvis agent:\nVeronica, which way should the bench face?"+groupReplyInstruction(nil, "Veronica") {
		t.Fatalf("handoff lost latest agent: %+v", last)
	}
	second := requests[1].Messages
	if last := second[len(second)-1]; last.Role != "user" || last.Content != "Unknown user:\nKeep the path clear."+groupReplyInstruction(nil, "Veronica")+currentRequestReminder(o.cfg, StreamOptions{}) {
		t.Fatalf("follow-up lost latest user: %+v", last)
	}
	if len(o.History()) != len(history)+3 {
		t.Fatalf("history length=%d", len(o.History()))
	}
}
