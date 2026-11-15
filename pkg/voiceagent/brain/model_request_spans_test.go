package brain_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/ollama/ollama/api"
)

// Requests a conversation's brain makes outside its own turn loop still reach
// the pipeline as model_request spans.
func TestConductorSideRequestsAreTraced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		content := "The task finished."
		if len(req.Format) > 0 {
			content = `{"kind":"yield","member_key":"","reason":"no_contribution"}`
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: content}, Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 40, EvalCount: 5}})
	}))
	defer server.Close()
	root := t.TempDir()
	provider, err := brain.NewOllama(&config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: root, WorkDir: root, MaxHistory: 10, OllamaNumCtx: 8192})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var mu sync.Mutex
	var spans []events.PipelineTrace
	events.Subscribe(bus, func(e events.PipelineTrace) {
		if e.Span.Stage == "model_request" {
			mu.Lock()
			spans = append(spans, e)
			mu.Unlock()
		}
	})
	p := &pipeline.Pipeline{Brain: provider, Events: bus}
	if err := p.BeginConversationIdentity("conductor-requests"); err != nil {
		t.Fatal(err)
	}
	p.ObserveModelRequests()
	runtime := p.ConversationScope().RuntimeEpoch

	if _, err := provider.DelegationSummarizer()(context.Background(), "worker output"); err != nil {
		t.Fatal(err)
	}
	decider, err := provider.DiscussionDecider()
	if err != nil {
		t.Fatal(err)
	}
	turn := "group-turn"
	trace := p.CaptureTrace()
	trace.TurnID = &turn
	if _, err := decider.Next(events.WithTrace(t.Context(), trace), discussion.Context{}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(spans) != 4 {
		t.Fatalf("spans = %d, want a start and an end for each request", len(spans))
	}
	for i, want := range []struct {
		purpose, state string
		turn           *string
	}{
		{brain.PurposeDelegationSummary, "start", nil},
		{brain.PurposeDelegationSummary, "end", nil},
		{brain.PurposeDiscussion, "start", &turn},
		{brain.PurposeDiscussion, "end", &turn},
	} {
		e := spans[i]
		if e.Span.Request == nil || e.Span.Request.Purpose != want.purpose || e.Span.State != want.state || e.Trace.RuntimeID != runtime {
			t.Fatalf("span %d = %+v in %+v, want %s %s", i, e.Span, e.Trace, want.purpose, want.state)
		}
		if (want.turn == nil) != (e.Span.ParentID == nil) || want.turn != nil && *e.Span.ParentID != turn {
			t.Fatalf("span %d parent = %v, want %v", i, e.Span.ParentID, want.turn)
		}
		if e.Span.State == "end" && (e.Span.Status != "completed" || e.Span.ID != spans[i-1].Span.ID) {
			t.Fatalf("span %d did not end its own request: %+v", i, e.Span)
		}
	}
}
