package personaruntime

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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

// The warmup exists to put the first turn's prompt in the server's cache. In a
// conversation with several personas the first route request carries the group
// roster, so a warmup sent before the roster reached the brain primed a prompt
// that diverged halfway through the system message.
func TestWarmupSendsTheFirstTurnsPrefix(t *testing.T) {
	var mu sync.Mutex
	var chats []api.ChatRequest
	warmed := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		case "/api/chat":
		default:
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
		resp := api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Fine, thanks."}, Done: true, DoneReason: "stop"}
		if len(req.Tools) > 0 {
			resp.Message = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call-1", Function: api.ToolCallFunction{Name: "route", Arguments: routeArgs("chat")}}}}
		}
		_ = json.NewEncoder(w).Encode(resp)
		if req.Options["num_predict"] == float64(1) {
			warmed <- struct{}{}
		}
	}))
	defer server.Close()

	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, entry := range []struct{ id, name string }{{"jarvis", "Jarvis"}, {"santa", "Santa"}} {
		profile := testProfile(entry.id, entry.name, entry.id)
		profile.Brain.Model = "fixture:latest"
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, prompts, entry.id, "You are {agent_name}.")
	}
	cfg := &config.Config{ActivePersona: "jarvis", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: prompts, WorkDir: root,
		MaxHistory: 20, OllamaNumCtx: 8192, OllamaNumBatch: 128, OllamaNumBatchExplicit: true, VoiceToolsEnabled: true, VoiceRouterTools: "route_only"}
	binding, err := catalog.ResolveBinding(cfg, "jarvis")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus(), VoiceToolsEnabled: true, RouterToolsPolicy: brain.NormalizeRouterToolsPolicy(cfg.VoiceRouterTools)}
	t.Cleanup(func() { _ = p.CloseBrains() })
	selector, err := NewSelector(SelectionOptions{Base: cfg, Catalog: catalog, Pipeline: p, SessionID: "warmup", Initial: binding,
		Persist: func([]brain.Turn, events.PersonaSelection) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = selector.Route

	selector.Warmup(t.Context())
	warmups := 0
	deadline := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case <-warmed:
			warmups++
		case <-time.After(500 * time.Millisecond):
			done = warmups > 0
		case <-deadline:
			t.Fatal("no warmup request")
		}
	}
	if err := p.RunTurnTextMode(t.Context(), "How's it going?"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chats) != warmups+2 {
		t.Fatalf("requests = %d, want %d warmup, one route and one reply", len(chats), warmups)
	}
	route, reply := chats[warmups], chats[warmups+1]
	if len(route.Tools) == 0 || len(reply.Tools) != 0 {
		t.Fatalf("route tools = %d, reply tools = %d; want the route round to carry tools and the reply none", len(route.Tools), len(reply.Tools))
	}
	samePrefix(t, "route", chats[0], route)
	if warmups > 1 {
		samePrefix(t, "reply", chats[1], reply)
	}
}

// samePrefix fails unless warm and live agree on everything a prompt cache
// keys on ahead of the newest user message.
func samePrefix(t *testing.T, name string, warm, live api.ChatRequest) {
	t.Helper()
	if len(warm.Messages) == 0 || len(live.Messages) == 0 || warm.Messages[0].Role != "system" {
		t.Fatalf("%s: warmup messages %d, live messages %d", name, len(warm.Messages), len(live.Messages))
	}
	if warm.Messages[0].Content != live.Messages[0].Content {
		t.Fatalf("%s: system message differs\nwarmup: %q\nlive:   %q", name, warm.Messages[0].Content, live.Messages[0].Content)
	}
	if !reflect.DeepEqual(warm.Tools, live.Tools) {
		t.Fatalf("%s: tools differ\nwarmup: %+v\nlive:   %+v", name, warm.Tools, live.Tools)
	}
	if !reflect.DeepEqual(warm.Think, live.Think) || !reflect.DeepEqual(warm.KeepAlive, live.KeepAlive) {
		t.Fatalf("%s: think %v/%v keep_alive %v/%v", name, warm.Think, live.Think, warm.KeepAlive, live.KeepAlive)
	}
	wantOpts, liveOpts := maps.Clone(warm.Options), maps.Clone(live.Options)
	delete(wantOpts, "num_predict")
	delete(liveOpts, "num_predict")
	if !reflect.DeepEqual(wantOpts, liveOpts) {
		t.Fatalf("%s: options differ: warmup %v, live %v", name, warm.Options, live.Options)
	}
}

func routeArgs(kind string) api.ToolCallFunctionArguments {
	args := api.NewToolCallFunctionArguments()
	args.Set("kind", kind)
	return args
}
