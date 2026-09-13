package personaruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	var answers [][]api.Message
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
			var address brain.AddressRequest
			if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &address); err != nil {
				t.Error(err)
				return
			}
			decision := brain.Addressee{Kind: "address", Name: "Jarvis"}
			if address.Account == "agent" && address.Speaker == "Jarvis" {
				decision.Name = "Santa"
			}
			raw, _ := json.Marshal(decision)
			content = string(raw)
			for _, participant := range address.Participants {
				if strings.Contains(participant.Name, "opaque") {
					t.Error("storage ID exposed to classifier")
				}
			}
		} else {
			answers = append(answers, req.Messages)
			content = "Santa, what do you think about cobalt?"
			if strings.Contains(req.Messages[0].Content, "You are Santa.") {
				content = "Cobalt is a good choice. Jarvis, any concerns?"
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
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if users != 1 || len(saved.Turns) != 3 || len(answers) != 2 || strings.Join(responders, ",") != "Jarvis,Santa" {
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
}
