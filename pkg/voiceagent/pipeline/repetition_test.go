package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/ollama/ollama/api"
)

// Exercise the real provider, segmenter, synthesis admission and session store.
// The rejected reply must never reach TTS, the UI envelope, or persisted turns.
func TestRepeatedOllamaReplyIsRecoveredBeforeSynthesisAndPersistence(t *testing.T) {
	prior := "The garden has blue flowers, two apple trees and a pond. We can add a bench by the pond and lavender along the path. A wooden fence would shelter the beds from wind. Leave room near the gate for a wheelbarrow and keep the stepping stones level."
	fresh := "The bench should cost about one hundred dollars."
	generation := 0
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
		text := `{"decision":"unrequested"}`
		if len(req.Format) == 0 {
			generation++
			text = fresh
			if generation == 1 {
				text = prior + " The bench can face west."
			}
			if generation > 2 {
				t.Error("unexpected generation retry")
			}
		}
		for len(text) > 0 {
			n := min(13, len(text))
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: text[:n]}})
			text = text[n:]
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
	}))
	defer server.Close()
	root := t.TempDir()
	cfg := &config.Config{ActivePersona: "veronica", AgentName: "Veronica", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: filepath.Join(root, "prompts"), WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192}
	provider, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ImportHistory([]brain.Turn{{Role: "user", Content: "Describe the garden."}, {Role: "assistant", Content: prior, PersonaID: "veronica", ResponseID: "old"}}); err != nil {
		t.Fatal(err)
	}
	synthesizer := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	bus := events.NewBus()
	var response string
	events.Subscribe(bus, func(e events.ResponseReady) { response = e.Response })
	p := &Pipeline{STT: &fakeSTT{text: "How much would the bench cost?"}, Brain: provider, TTS: synthesizer, Player: player, Events: bus}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(synthesizer.Texts(), " "); got != fresh {
		t.Fatalf("synthesized=%q", got)
	}
	if response != fresh {
		t.Fatalf("displayed=%q", response)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "fixture:latest")
	if err := store.Save(sess, provider.History()); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) != 4 || saved.Turns[3].Content != fresh {
		t.Fatalf("persisted turns=%+v", saved.Turns)
	}
}
