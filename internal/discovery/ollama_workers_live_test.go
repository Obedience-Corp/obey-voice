//go:build integration

package discovery

import (
	"os"
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestLiveOllamaWorkerCapabilityFilter(t *testing.T) {
	if os.Getenv("OBEY_VOICE_LIVE_OLLAMA_CATALOG") == "" {
		t.Skip("set OBEY_VOICE_LIVE_OLLAMA_CATALOG=1 with Dolphin and LFM3 installed locally")
	}
	p := discoverOllama(t.Context(), &config.Config{OllamaHost: "http://localhost:11434"})
	t.Logf("brain models=%d verified worker models=%v", len(p.Models), p.WorkerModels)
	if !p.Available || !slices.Contains(p.Models, "dolphin-mistral") || !slices.Contains(p.WorkerModels, "lfm3:1.7b") || slices.Contains(p.WorkerModels, "dolphin-mistral") {
		t.Fatalf("live catalog did not filter tools correctly: %+v", p)
	}
}
