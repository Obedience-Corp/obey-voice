//go:build integration

package personaruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// This opt-in proof uses an installed local model, real selection, provider
// requests, and disk persistence. It neither captures nor plays audio.
func TestLivePersonaThreadHandoff(t *testing.T) {
	if os.Getenv("OBEY_VOICE_LIVE_PERSONA") != "1" {
		t.Skip("set OBEY_VOICE_LIVE_PERSONA=1 with an installed local Ollama model")
	}
	model := os.Getenv("OBEY_VOICE_LIVE_PERSONA_MODEL")
	if model == "" {
		model = "qwen3.8:latest"
	}
	root := t.TempDir()
	// Keep sampling fixed for a reproducible model acceptance check. Routing,
	// prompt construction, streaming, and persistence use production code.
	endpoint, err := url.Parse("http://localhost:11434")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(endpoint)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			options, _ := body["options"].(map[string]any)
			if options == nil {
				options = map[string]any{}
			}
			options["temperature"], options["seed"] = 0, 42
			body["options"] = options
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(encoded))
			r.ContentLength = int64(len(encoded))
		}
		proxy.ServeHTTP(w, r)
	}))
	defer host.Close()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, entry := range []struct{ id, name string }{{"obey", "Obey"}, {"samantha", "Samantha"}} {
		p := testProfile(entry.id, entry.name, entry.id)
		p.Brain.Model = model
		writeTestProfile(t, catalog.Dir, p)
		writeTestPrompt(t, prompts, entry.id, "You are {agent_name}. Speak as yourself. Answer plainly in one or two sentences.")
	}
	cfg := &config.Config{ActivePersona: "obey", BrainProvider: "ollama", OllamaModel: model, OllamaHost: host.URL, PromptsDir: prompts, WorkDir: root, MaxHistory: 30, OllamaNumCtx: 8192, OllamaThink: "auto"}
	binding, err := catalog.ResolveBinding(cfg, "obey")
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
	sess := session.New("ollama", model)
	selector, err := NewSelector(SelectionOptions{Base: cfg, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
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
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	if err := p.RunTurnTextMode(ctx, "Remember that our shared code is cobalt."); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Obey", "Samantha", "Obey"} {
		for _, check := range []struct{ input, want string }{
			{name + ", who are you?", name},
			{"What is the shared code we discussed?", "cobalt"},
		} {
			if err := p.RunTurnTextMode(ctx, check.input); err != nil {
				t.Fatal(err)
			}
			history := p.Brain.History()
			reply := history[len(history)-1]
			t.Logf("%s: %s", name, reply.Content)
			if reply.PersonaDisplayName != name || !strings.Contains(strings.ToLower(reply.Content), strings.ToLower(check.want)) || strings.Contains(reply.Content, "Historical assistant:") {
				t.Fatalf("identity/context failed: %+v", reply)
			}
			if reply.ResponseID == "" || strings.Contains(reply.Content, reply.ResponseID) {
				t.Fatalf("invalid response traceability: %+v", reply)
			}
		}
	}
	stored, err := store.Load(sess.ID)
	if err != nil || len(stored.Turns) != 14 || stored.SelectedPersonaID != "obey" {
		t.Fatalf("saved shared conversation: %+v, %v", stored, err)
	}
	// Reproduce a saved thread already contaminated by the old implementation.
	// Its transcript remains untouched, while the resumed model must recover
	// its selected identity and stop imitating the internal history headers.
	legacy := "[Historical assistant: Obey response OLDRESPONSE1]\n[Historical assistant: Obey response OLDRESPONSE2]\nYeah, I am still Obey."
	legacyIndex := len(stored.Turns) + 1
	stored.Turns = append(stored.Turns,
		brain.Turn{Role: "user", Content: "Aren't you Samantha?"},
		brain.Turn{Role: "assistant", Content: legacy, PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "legacy-response"},
	)
	if err := store.Save(sess, stored.Turns); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Brain.LoadHistory(stored.Turns)
	selector.RestoreSession(sess.ID, "samantha", "Samantha", "")
	for _, compact := range []bool{false, true} {
		if compact {
			p.CompactPrompt = "Summarize the shared facts and who said them for continuing this conversation."
			if err := p.CompactConversation(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, check := range []struct{ input, want string }{
			{"Samantha, who are you?", "Samantha"},
			{"What is the shared code we discussed?", "cobalt"},
		} {
			if err := p.RunTurnTextMode(ctx, check.input); err != nil {
				t.Fatal(err)
			}
			history := p.Brain.History()
			reply := history[len(history)-1]
			t.Logf("Samantha resumed (compacted=%v): %s", compact, reply.Content)
			if !strings.Contains(strings.ToLower(reply.Content), strings.ToLower(check.want)) || strings.Contains(reply.Content, "Historical assistant:") || strings.Contains(reply.Content, "OLDRESPONSE") {
				t.Fatalf("resumed identity/context failed: %+v", reply)
			}
			if history[legacyIndex].Content != legacy {
				t.Fatal("recovery rewrote the historical transcript")
			}
		}
	}
	// A real model-generated question hands the floor to the other persona.
	// No new human input is invented between the two assistant replies.
	p.RouteHandoff = selector.RouteHandoff
	before := len(p.Brain.History())
	if err := p.RunTurnTextMode(ctx, "Samantha, ask Obey what he thinks about our code. Ask him directly by name and let him answer."); err != nil {
		t.Fatal(err)
	}
	floor := p.Brain.History()[before:]
	for _, reply := range floor {
		t.Logf("group floor %s %s: %s", reply.Role, reply.PersonaDisplayName, reply.Content)
	}
	if len(floor) != 3 || floor[0].Role != "user" || floor[1].PersonaID != "samantha" || floor[2].PersonaID != "obey" || !strings.Contains(strings.ToLower(floor[2].Content), "cobalt") {
		t.Fatalf("agent floor/context failed: %+v", floor)
	}

}
