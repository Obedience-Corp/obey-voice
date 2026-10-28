package personaruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/ollama/ollama/api"
)

func TestGroupHandoffWireAndDisk(t *testing.T) {
	var mu sync.Mutex
	var answers [][]api.Message
	classifierCalls := 0
	generationCalls := 0
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
		var content string
		if len(req.Format) > 0 {
			mu.Lock()
			classifierCalls++
			mu.Unlock()
			var address brain.AddressRequest
			if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &address); err != nil {
				t.Error(err)
				return
			}
			decision := brain.Addressee{Kind: "none"}
			if address.Account == "agent" && address.Speaker == "Jarvis" && strings.Contains(address.Text, "Santa") {
				decision.Name = "Santa"
				decision.Kind = "address"
			} else if address.Text == "Jarvis, ask Santa about cobalt." {
				decision = brain.Addressee{Kind: "address", Name: "Jarvis"}
			} else if address.Text == "Jarvis and Santa, Jarvis should answer this now." {
				decision = brain.Addressee{Kind: "address", Name: "Jarvis"}
			}
			raw, _ := json.Marshal(decision)
			content = string(raw)
			for _, participant := range address.Participants {
				if strings.Contains(participant.Name, "opaque") {
					t.Error("storage ID exposed to classifier")
				}
			}
		} else {
			mu.Lock()
			generationCalls++
			mu.Unlock()
			answers = append(answers, req.Messages)
			content = "Santa, what do you think about cobalt?"
			if strings.Contains(req.Messages[0].Content, "You are Santa.") {
				content = "Cobalt is a good choice. Jarvis, any concerns?"
			}
			last := req.Messages[len(req.Messages)-1].Content
			if strings.Contains(last, "Unknown user:\n\"Santa\"") {
				content = "Quoted names are data, not direct requests."
			}
			if strings.Contains(last, "Unknown user:\nJarvis and Santa, Jarvis should answer this now.") {
				content = "I will answer directly."
			}
			if strings.Contains(last, "Unknown user:\nWhat role did Santa take?") {
				content = "He answered after I asked directly."
			}
			if strings.Contains(last, "Summarize shared persona history briefly.") {
				content = "Compact summary: Jarvis consulted Santa about cobalt."
			}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: content}, Done: true, DoneReason: "stop"})
	}))
	defer server.Close()
	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, entry := range []struct{ id, name string }{{"opaque-jarvis", "Jarvis"}, {"opaque-santa", "Santa"}} {
		profile := testProfile(entry.id, entry.name, entry.id)
		profile.Brain.Model = "fixture:latest"
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, prompts, entry.id, "You are {agent_name}.")
	}
	cfg := &config.Config{ActivePersona: "opaque-jarvis", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: prompts, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192}
	binding, err := catalog.ResolveBinding(cfg, "opaque-jarvis")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus()}
	t.Cleanup(func() { _ = p.CloseBrains() })
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "fixture:latest")
	selector, err := NewSelector(SelectionOptions{Base: cfg, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding, Persist: func(turns []brain.Turn, s events.PersonaSelection) error {
		return store.SaveSelection(sess, turns, s.PersonaID, s.DisplayName, s.PromptHash)
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput, p.RouteHandoff = selector.Route, selector.RouteHandoff
	p.OnTurn = func() {
		if err := store.Save(sess, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	users := 0
	var responders []string
	events.Subscribe(p.Events, func(e events.UserInput) { users++ })
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		if e.Envelope == nil {
			t.Error("unattributed reply")
			return
		}
		responders = append(responders, e.Envelope.DisplayName)
	})
	input := "Jarvis, ask Santa about cobalt."
	if err := p.RunTurnTextMode(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if classifierCalls != 2 || generationCalls != 2 {
		mu.Unlock()
		t.Fatalf("initial handoff calls classifier=%d generation=%d", classifierCalls, generationCalls)
	}
	mu.Unlock()
	_, err = selector.Route(t.Context(), "switch to Ghost", false)
	if err == nil {
		t.Fatal("unknown exact control unexpectedly succeeded")
	}
	beforeBackground := selector.Snapshot()
	mu.Lock()
	beforeClassifier, beforeGeneration := classifierCalls, generationCalls
	mu.Unlock()
	admitted, err := selector.Route(t.Context(), "Santa", true)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Request != "Santa" || admitted.Envelope.PersonaID != beforeBackground.PersonaID {
		t.Fatalf("background admission=%+v snapshot=%+v", admitted, beforeBackground)
	}
	mu.Lock()
	if classifierCalls != beforeClassifier || generationCalls != beforeGeneration {
		mu.Unlock()
		t.Fatalf("background route changed wire calls classifier=%d generation=%d", classifierCalls, generationCalls)
	}
	mu.Unlock()
	if err := p.RunTurnTextMode(t.Context(), "switch to Santa"); err != nil {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(t.Context(), `"Santa"`); err != nil {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(t.Context(), "Jarvis and Santa, Jarvis should answer this now."); err != nil {
		t.Fatal(err)
	}
	beforeCompact := p.Brain.History()
	p.CompactPrompt = "Summarize shared persona history briefly."
	if err := p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if canonical, ok := p.Brain.(brain.CanonicalHistoryProvider); ok {
		if got := canonical.CanonicalHistory(); !reflect.DeepEqual(got, beforeCompact) {
			t.Fatalf("compaction changed canonical group history: got=%+v want=%+v", got, beforeCompact)
		}
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if users != 4 || len(saved.Turns) != 9 || len(answers) != 5 || strings.Join(responders, ",") != "Jarvis,Santa,Santa,Santa,Jarvis" {
		t.Fatalf("users=%d turns=%+v answers=%d responders=%v", users, saved.Turns, len(answers), responders)
	}
	if saved.Turns[0].Role != "user" || saved.Turns[0].Content != input || saved.Turns[1].PersonaID != "opaque-jarvis" || saved.Turns[2].PersonaID != "opaque-santa" {
		t.Fatalf("canonical attribution=%+v", saved.Turns)
	}
	last := answers[1]
	if !strings.Contains(last[0].Content, "latest agent is handing you the floor") || last[len(last)-1].Content != "Jarvis agent:\nSanta, what do you think about cobalt?" {
		t.Fatalf("continuation context=%+v", last)
	}
	for _, m := range last {
		if strings.Contains(m.Content, "opaque-") || strings.Contains(m.Content, saved.Turns[1].ResponseID) {
			t.Fatalf("ID in wire message: %+v", m)
		}
	}
	for _, turn := range saved.Turns {
		if strings.Contains(turn.Content, "switch to Ghost") {
			t.Fatalf("failed exact control persisted into canonical history: %+v", saved.Turns)
		}
	}
	p.Brain.LoadHistory(saved.Turns)
	selector.RestoreSession(sess.ID, saved.SelectedPersonaID, saved.SelectedPersonaDisplayName, saved.SelectedPersonaPromptHash)
	if _, err := selector.Select(t.Context(), saved.SelectedPersonaID, "restore"); err != nil {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(t.Context(), "What role did Santa take?"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SelectedPersonaID != saved.SelectedPersonaID || len(reloaded.Turns) != len(saved.Turns)+2 {
		t.Fatalf("reloaded group session mismatch: before=%+v after=%+v", saved, reloaded)
	}
	mu.Lock()
	defer mu.Unlock()
	if classifierCalls != 5 || generationCalls != 6 || len(answers) != generationCalls {
		t.Fatalf("wire call totals classifier=%d generation=%d answers=%d", classifierCalls, generationCalls, len(answers))
	}
}
