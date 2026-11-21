package personaruntime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/ollama/ollama/api"
)

// A persona's brain.router decides its turns even when the installation's
// voice_router_tools differs, in both directions of a switch. The warmup
// primes the single request a single_call chat turn sends.
func TestPersonaRouterDecidesItsTurnsAndWarmup(t *testing.T) {
	var mu sync.Mutex
	var chats []api.ChatRequest
	warmed := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		if r.URL.Path != "/api/chat" {
			_, _ = io.Copy(io.Discard, r.Body)
			http.NotFound(w, r)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		chats = append(chats, req)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Fine, thanks."}, Done: true, DoneReason: "stop"})
		if req.Options["num_predict"] == float64(1) {
			warmed <- struct{}{}
		}
	}))
	defer server.Close()

	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, entry := range []struct{ id, name, router string }{{"obey", "Obey", brain.SingleCallSetting}, {"santa", "Santa", ""}} {
		profile := testProfile(entry.id, entry.name, entry.id)
		profile.Brain.Model = "fixture:latest"
		profile.Brain.Router = entry.router
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, prompts, entry.id, "You are {agent_name}.")
	}
	cfg := &config.Config{ActivePersona: "obey", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: prompts, WorkDir: root,
		MaxHistory: 20, OllamaNumCtx: 8192, OllamaNumBatch: 128, OllamaNumBatchExplicit: true, VoiceToolsEnabled: true, VoiceRouterTools: "direct"}
	binding, err := catalog.ResolveBinding(cfg, "obey")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	// As voiceagent.New builds it from the bound persona's configuration.
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus(), VoiceToolsEnabled: true, RouterToolsPolicy: brain.NormalizeRouterToolsPolicy(binding.Config().VoiceRouterTools)}
	t.Cleanup(func() { _ = p.CloseBrains() })
	selector, err := NewSelector(SelectionOptions{Base: cfg, Catalog: catalog, Pipeline: p, SessionID: "single-call", Initial: binding,
		Persist: func([]brain.Turn, events.PersonaSelection) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = selector.Route

	selector.Warmup(t.Context())
	for range 2 {
		select {
		case <-warmed:
		case <-time.After(5 * time.Second):
			t.Fatal("want two warmup requests")
		}
	}
	turn := func(text string) []api.ChatRequest {
		t.Helper()
		mu.Lock()
		before := len(chats)
		mu.Unlock()
		if err := p.RunTurnTextMode(t.Context(), text); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]api.ChatRequest(nil), chats[before:]...)
	}
	routeDescription := func(req api.ChatRequest) string {
		for _, tool := range req.Tools {
			if tool.Function.Name == "route" {
				return tool.Function.Description
			}
		}
		return ""
	}

	first := turn("How's it going?")
	if len(first) != 1 || !strings.HasPrefix(routeDescription(first[0]), "Hand this user turn") {
		t.Fatalf("single_call turn sent %d request(s); tools %+v", len(first), first[0].Tools)
	}
	mu.Lock()
	warmups := append([]api.ChatRequest(nil), chats[:2]...)
	mu.Unlock()
	// Both warmups carry the turn's prefix and differ only in the user's words,
	// so the cache keeps a resume point where turn 1's own words begin.
	for i, warm := range warmups {
		samePrefix(t, "single_call", warm, first[0])
		if len(warm.Messages) != 2 || (i == 1 && warm.Messages[1].Content == warmups[0].Messages[1].Content) {
			t.Fatalf("warmup %d messages: %+v", i, warm.Messages)
		}
	}
	if !strings.Contains(first[0].Messages[0].Content, "answer directly in plain speech") {
		t.Fatal("the system prompt does not describe single_call")
	}

	if _, err := selector.Select(t.Context(), "santa", "ui"); err != nil {
		t.Fatal(err)
	}
	direct := turn("And you?")
	if len(direct) != 1 || routeDescription(direct[0]) != "" || len(direct[0].Tools) < 2 {
		t.Fatalf("a persona without brain.router must follow voice_router_tools (direct): %+v", direct)
	}

	if _, err := selector.Select(t.Context(), "obey", "ui"); err != nil {
		t.Fatal(err)
	}
	back := turn("Back again.")
	if len(back) != 1 || !strings.HasPrefix(routeDescription(back[0]), "Hand this user turn") {
		t.Fatalf("switching back did not restore single_call: %+v", back)
	}
}
