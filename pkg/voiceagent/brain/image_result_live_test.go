//go:build integration

package brain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Opt-in inference against an installed local model. No tools execute, no
// workers dispatch, and personal configuration/history are never modified.
func TestLiveImageRequestsRouteToWorker(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_IMAGE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_IMAGE_ROUTING_MODEL to an installed local model")
	}
	for _, tc := range []struct {
		name, input string
		history     []Turn
	}{
		{name: "direct", input: "Can you show me an image of a dog?"},
		{name: "format", input: "Uh just find a P and G of a dog.", history: []Turn{{Role: "user", Content: "Can you show me an image of a dog?"}, {Role: "assistant", Content: "Which image format do you want?"}}},
		{name: "missing", input: "I don't see the image. It needs to be in this chat.", history: []Turn{{Role: "user", Content: "Find a PNG of a dog."}, {Role: "assistant", Content: missingDogImages}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			root := t.TempDir()
			b, err := NewOllama(&config.Config{
				OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 32768,
				VoiceToolsEnabled: true, VoiceRouterTools: string(RouterToolsRouteOnly),
				AgentName: "Obey", MaxHistory: 20, WorkDir: root,
				PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.ImportHistory(tc.history); err != nil {
				t.Fatal(err)
			}
			// Match the reported session's legacy worker configuration. Set
			// OBEY_VOICE_IMAGE_ROUTING_TIERED=1 to exercise tiered admission too.
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			if os.Getenv("OBEY_VOICE_IMAGE_ROUTING_TIERED") == "1" {
				opts.WorkerTiers = &WorkerTierAvailability{Simple: true, Medium: true, Complex: true}
			}
			stream, err := b.ThinkStream(ctx, tc.input, opts)
			if err != nil {
				t.Fatal(err)
			}
			spoken, _ := drainStream(t, stream)
			result := <-stream.Done
			t.Logf("route=%q tier=%q spoken=%q error=%v", result.Route, result.RouteTier, spoken, result.Err)
			if result.Err != nil || result.Route != "spawn" || (opts.WorkerTiers != nil && result.RouteTier == "") || spoken != "" {
				t.Fatalf("image request did not reach worker routing: %+v", result)
			}
		})
	}
}

// Opt-in worker protocol check with real local inference and filesystem tools
// in a disposable directory. It does not search or download from the web.
func TestLiveImageWorkerReturnsAttachments(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_IMAGE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_IMAGE_ROUTING_MODEL to an installed local model")
	}
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	for name, data := range map[string][]byte{"fixture.png": testmedia.PNG(), "fixture.gif": testmedia.GIF()} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewOllama(&config.Config{
		OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 32768,
		VoiceToolsEnabled: true, AgentName: "Obey", MaxHistory: 20, WorkDir: root,
		PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	reply, err := b.ThinkFull(ctx, "Find the existing fixture.png and fixture.gif in your current working directory, verify that they exist using your tools, and attach both files in this chat. Do not change the files.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsLegacy})
	if err != nil {
		t.Fatal(err)
	}
	_, refs, err := chatmedia.DefaultStore().ImportReply(reply, root)
	t.Logf("attachments=%d reply=%q", len(refs), reply)
	if err != nil || len(refs) != 2 {
		t.Fatalf("worker did not deliver both files: %v", err)
	}
}
