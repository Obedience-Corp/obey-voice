package personaruntime

import (
	"context"
	"encoding/json"
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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// This exercises the actual Ollama projection and pipeline against controlled
// HTTP. It proves request/storage attribution, not model recall or real audio.
func TestSelectorProductionPipelineWireAndDisk(t *testing.T) {
	var mu sync.Mutex
	var requests []struct {
		Messages []struct{ Role, Content string }
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		if r.URL.Path != "/api/chat" {
			http.Error(w, "unexpected endpoint", 404)
			return
		}
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": "The code is cobalt."}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for _, id := range []string{"samantha", "santa"} {
		profile := testProfile(id, map[string]string{"samantha": "Samantha", "santa": "Santa"}[id], id)
		profile.Brain.Model = ""
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	base := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", OllamaModel: "fixture:latest", OllamaHost: server.URL, PromptsDir: promptDir, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192}
	binding, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus()}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "fixture:latest")
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		Persist: func(turns []brain.Turn, s events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, s.PersonaID, s.DisplayName, s.PromptHash)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = selector.Route
	p.OnTurn = func() {
		if err := store.Save(sess, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	var identities []events.ResponseEnvelope
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		if e.Envelope == nil {
			t.Error("reply lacks identity")
		} else {
			identities = append(identities, *e.Envelope)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for i, input := range []string{"The code is cobalt.", "Santa.", "Can you recall the code?", "Samantha, what is it?"} {
		if err := p.RunTurnTextMode(ctx, input); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			mu.Lock()
			calls := len(requests)
			mu.Unlock()
			if calls != 1 {
				t.Fatalf("bare name called model: %d", calls)
			}
		}
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "samantha" || len(loaded.Turns) != 8 {
		t.Fatalf("disk selection/history: %+v", loaded)
	}
	if len(identities) != 4 {
		t.Fatalf("replies=%d", len(identities))
	}
	for i, want := range []string{"samantha", "santa", "santa", "samantha"} {
		turn := loaded.Turns[i*2+1]
		if identities[i].PersonaID != want || turn.PersonaID != want || identities[i].ResponseID != turn.ResponseID || turn.PromptHash == "" {
			t.Fatalf("reply %d identity/disk mismatch: %+v / %+v", i, identities[i], turn)
		}
	}
	lastInput := loaded.Turns[6]
	if lastInput.Content != "Samantha, what is it?" || lastInput.RoutedInput != "what is it?" {
		t.Fatalf("original/routed input: %+v", lastInput)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("model calls=%d", len(requests))
	}
	last := requests[2].Messages
	if last[len(last)-1].Content != "what is it?" {
		t.Fatalf("routed request missing: %+v", last)
	}
	found := false
	for _, message := range last {
		if message.Role == "assistant" && strings.Contains(message.Content, "Historical assistant: Santa") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Santa attribution missing from actual request: %+v", last)
	}
}
