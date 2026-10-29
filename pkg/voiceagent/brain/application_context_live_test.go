//go:build integration

package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Uses a disposable install and the local model, without tools, live sessions,
// microphone capture, synthesis or playback.
func TestLiveApplicationCapabilityGrounding(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_APP_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_APP_MODEL to an installed local Ollama model")
	}
	root := t.TempDir()
	promptDir := filepath.Join(root, "prompts", "persona")
	if err := os.MkdirAll(promptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	persona := filepath.Join(promptDir, "venessa.md")
	if err := os.WriteFile(persona, []byte("You are {agent_name}. Answer conversationally and accurately in two or three sentences."), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewOllama(&config.Config{OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 8192,
		AgentName: "Venessa", Persona: "venessa", WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 12})
	if err != nil {
		t.Fatal(err)
	}
	b.SetGroupParticipants([]GroupParticipant{{Name: "Obey"}, {Name: "Venessa"}, {Name: "Joker"}})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	response, err := b.ThinkFull(ctx, "Does Obey Voice have a built-in way to convert a Calibre ebook into audio? Is Venessa already available here?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%s response=%s", model, response)
	if !strings.Contains(strings.ToLower(response), "audiobook") || !strings.Contains(strings.ToLower(response), "venessa") {
		t.Fatalf("missing native capability/persona grounding: %q", response)
	}
	for _, wrong := range []string{"vhs-private-gist", "elevenlabs", "azure", "persona create", "create venessa", "new persona"} {
		if strings.Contains(strings.ToLower(response), wrong) {
			t.Errorf("invented workflow or missing existing persona: %q", response)
		}
	}
}
