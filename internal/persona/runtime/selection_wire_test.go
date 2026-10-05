package personaruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	classifierCalls := 0
	generationCalls := 0
	var requests []struct {
		Messages []struct{ Role, Content string }
		Format   json.RawMessage
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
			Format   json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Format) > 0 {
			mu.Lock()
			classifierCalls++
			mu.Unlock()
			var input brain.AddressRequest
			if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &input); err != nil {
				t.Error(err)
				return
			}
			decision := brain.Addressee{Kind: "none"}
			switch input.Text {
			case "Could Samantha answer what code we shared?":
				decision = brain.Addressee{Kind: "address", Name: "Samantha"}
			case "Samantha and Santa, Samantha should answer this.":
				decision = brain.Addressee{Kind: "address", Name: "Samantha"}
			}
			raw, _ := json.Marshal(decision)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": string(raw)}, "done": true})
			return
		}
		mu.Lock()
		generationCalls++
		requests = append(requests, request)
		mu.Unlock()
		if len(request.Messages) > 0 && strings.Contains(request.Messages[len(request.Messages)-1].Content, "Summarize the preserved facts briefly.") {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": "Compact summary: the code is cobalt."}, "done": true, "done_reason": "stop"})
			return
		}
		name := "Santa"
		if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are Samantha.") {
			name = "Samantha"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": "I am " + name + ". The code is cobalt."}, "done": true, "done_reason": "stop"})
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
	mu.Lock()
	startClassifier, startGeneration := classifierCalls, generationCalls
	mu.Unlock()
	if err := p.RunTurnTextMode(ctx, "The code is cobalt."); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	firstClassifier := classifierCalls - startClassifier
	firstGeneration := generationCalls - startGeneration
	mu.Unlock()
	if firstClassifier != 1 || firstGeneration != 1 {
		t.Fatalf("initial turn calls classifier=%d generation=%d", firstClassifier, firstGeneration)
	}
	beforeRefusal := selector.Snapshot()
	if _, err := selector.Route(ctx, "switch to Unknown", false); err == nil {
		t.Fatal("unknown exact control unexpectedly succeeded")
	}
	mu.Lock()
	if classifierCalls != startClassifier+1 || generationCalls != startGeneration+1 {
		mu.Unlock()
		t.Fatalf("refused exact control changed wire calls classifier=%d generation=%d", classifierCalls, generationCalls)
	}
	mu.Unlock()
	if afterRefusal := selector.Snapshot(); !reflect.DeepEqual(afterRefusal, beforeRefusal) {
		t.Fatalf("refused exact control changed selection: before=%+v after=%+v", beforeRefusal, afterRefusal)
	}
	for i, input := range []string{
		"switch to Santa",
		"Can you recall the code?",
		"Could Samantha answer what code we shared?",
		`"Santa"`,
		"Samantha and Santa, Samantha should answer this.",
	} {
		if err := p.RunTurnTextMode(ctx, input); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			mu.Lock()
			classifierDelta := classifierCalls - (startClassifier + 1)
			generationDelta := generationCalls - (startGeneration + 1)
			mu.Unlock()
			if classifierDelta != 0 || generationDelta != 0 {
				t.Fatalf("exact control used wire calls classifier=%d generation=%d", classifierDelta, generationDelta)
			}
		}
	}
	beforeCompact := p.Brain.History()
	p.CompactPrompt = "Summarize the preserved facts briefly."
	if err := p.CompactConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if canonical, ok := p.Brain.(brain.CanonicalHistoryProvider); ok {
		if got := canonical.CanonicalHistory(); !reflect.DeepEqual(got, beforeCompact) {
			t.Fatalf("compaction changed canonical history: got=%+v want=%+v", got, beforeCompact)
		}
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "samantha" || len(loaded.Turns) != 12 {
		t.Fatalf("disk selection/history: %+v", loaded)
	}
	if len(identities) != 6 {
		t.Fatalf("replies=%d", len(identities))
	}
	for i, want := range []string{"samantha", "santa", "santa", "samantha", "samantha", "samantha"} {
		turn := loaded.Turns[i*2+1]
		if identities[i].PersonaID != want || turn.PersonaID != want || identities[i].ResponseID != turn.ResponseID || turn.PromptHash == "" {
			t.Fatalf("reply %d identity/disk mismatch: %+v / %+v", i, identities[i], turn)
		}
	}
	lastInput := loaded.Turns[10]
	if lastInput.Content != "Samantha and Santa, Samantha should answer this." || lastInput.RoutedInput != "" {
		t.Fatalf("original/routed input: %+v", lastInput)
	}
	for _, turn := range loaded.Turns {
		if strings.Contains(turn.Content, "switch to Unknown") {
			t.Fatalf("failed exact control persisted into canonical history: %+v", loaded.Turns)
		}
	}
	p.Brain.LoadHistory(loaded.Turns)
	selector.RestoreSession(sess.ID, loaded.SelectedPersonaID, loaded.SelectedPersonaDisplayName, loaded.SelectedPersonaPromptHash)
	if _, err := selector.Select(ctx, loaded.SelectedPersonaID, "restore"); err != nil {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(ctx, "What code are we tracking now?"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SelectedPersonaID != loaded.SelectedPersonaID || len(reloaded.Turns) != len(loaded.Turns)+2 {
		t.Fatalf("reloaded session mismatch after restore: before=%+v after=%+v", loaded, reloaded)
	}
	mu.Lock()
	defer mu.Unlock()
	if classifierCalls != 6 {
		t.Fatalf("classifier calls=%d", classifierCalls)
	}
	if generationCalls != 7 || len(requests) != generationCalls {
		t.Fatalf("generation calls=%d requests=%d", generationCalls, len(requests))
	}
	var routed []struct{ Role, Content string }
	for _, req := range requests {
		msgs := req.Messages
		if len(msgs) > 0 && msgs[len(msgs)-1].Content == "Unknown user:\nSamantha and Santa, Samantha should answer this." {
			routed = msgs
			break
		}
	}
	if len(routed) == 0 {
		t.Fatalf("routed request missing: %+v", requests)
	}
	found := false
	own := false
	for _, message := range routed {
		if message.Role != "assistant" && message.Role != "system" && strings.Contains(message.Content, "Santa") && strings.Contains(message.Content, "cobalt") {
			found = true
		}
		if message.Role == "assistant" {
			own = true
			if strings.Contains(message.Content, "I am Santa.") || strings.Contains(message.Content, "Historical assistant:") {
				t.Fatalf("another persona's identity leaked into Samantha's own thread: %+v", message)
			}
		}
		for _, response := range identities {
			if strings.Contains(message.Content, response.ResponseID) {
				t.Fatalf("internal response id exposed to model: %+v", message)
			}
		}
	}
	if !found || !own || !strings.Contains(routed[0].Content, "You are Samantha.") {
		t.Fatalf("Samantha's own thread/shared Santa context/current identity missing: %+v", routed)
	}
}
