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

// Explicit opt-in local probes: no personal config, microphone, speech output,
// model downloads or dispatched workers. A spawn result verifies admission only.
func TestLiveWorkerRouterModelCompatibility(t *testing.T) {
	if os.Getenv("OBEY_VOICE_LIVE_TOOLLESS_MODEL") == "" && os.Getenv("OBEY_VOICE_LIVE_ROUTING_MODEL") == "" {
		t.Skip("set OBEY_VOICE_LIVE_TOOLLESS_MODEL or OBEY_VOICE_LIVE_ROUTING_MODEL to an installed local model")
	}
	for _, tc := range []struct {
		env         string
		unsupported bool
	}{
		{"OBEY_VOICE_LIVE_TOOLLESS_MODEL", true},
		{"OBEY_VOICE_LIVE_ROUTING_MODEL", false},
	} {
		model := os.Getenv(tc.env)
		if model == "" {
			continue
		}
		t.Run(model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			root := t.TempDir()
			b, err := NewOllama(&config.Config{
				OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 4096,
				VoiceToolsEnabled: true, VoiceRouterTools: string(RouterToolsRouteOnly),
				AgentName: "Obey", MaxHistory: 12, WorkDir: root,
				PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"),
			})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := b.ThinkStream(ctx, "Can you spin up a worker to look up today's weather in Denver?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
			if err != nil {
				t.Fatal(err)
			}
			spoken, _ := drainStream(t, stream)
			result := <-stream.Done
			t.Logf("route=%q spoken=%q error=%v", result.Route, spoken, result.Err)
			if tc.unsupported {
				if result.Err == nil || !strings.Contains(result.Err.Error(), "Settings > Brain") || spoken != "" || result.Route != "" {
					t.Fatalf("unsupported model produced a normal reply: %+v / %q", result, spoken)
				}
			} else if result.Err != nil || result.Route != "spawn" {
				t.Fatalf("tool-capable model did not route to a worker: %+v", result)
			}
		})
	}
}
