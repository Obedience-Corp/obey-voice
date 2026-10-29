//go:build integration

package personaruntime

import (
	"context"
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

// Exercise a first greeting with real model classification, response generation,
// and persistence. All files are isolated; this proof never opens audio devices.
func TestLivePersonaOpeningGreeting(t *testing.T) {
	if os.Getenv("OBEY_VOICE_LIVE_PERSONA") != "1" {
		t.Skip("set OBEY_VOICE_LIVE_PERSONA=1 with an installed local Ollama model")
	}
	model := os.Getenv("OBEY_VOICE_LIVE_PERSONA_MODEL")
	if model == "" {
		model = "qwen3.8:latest"
	}
	root := t.TempDir()
	config.SetConfigDirForTest(t, root)
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, name := range []string{"Obey", "Samantha", "Santa", "Jarvis"} {
		id := strings.ToLower(name)
		profile := testProfile(id, name, id)
		profile.Brain.Model = model
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, prompts, id, "You are {agent_name}. Answer conversationally in one short sentence.")
	}
	cfg := &config.Config{ActivePersona: "obey", BrainProvider: "ollama", OllamaModel: model,
		OllamaHost: "http://localhost:11434", PromptsDir: prompts, WorkDir: root,
		MaxHistory: 30, OllamaNumCtx: 8192, OllamaThink: "off"}
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
	selector, err := NewSelector(SelectionOptions{Base: cfg, Catalog: catalog, Pipeline: p,
		SessionID: sess.ID, Initial: binding,
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
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	const greeting = "How's it going?"
	if err := p.RunTurnTextMode(ctx, greeting); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SelectedPersonaID != "obey" || len(stored.Turns) != 2 {
		t.Fatalf("saved conversation = %+v", stored)
	}
	user, reply := stored.Turns[0], stored.Turns[1]
	if user.Role != "user" || user.Content != greeting || reply.Role != "assistant" ||
		reply.PersonaID != "obey" || strings.TrimSpace(reply.Content) == "" || reply.Content == "Obey here." {
		t.Fatalf("greeting was not answered as a full Obey turn: %+v", stored.Turns)
	}
	t.Logf("Obey answered and persisted: %s", reply.Content)
}
