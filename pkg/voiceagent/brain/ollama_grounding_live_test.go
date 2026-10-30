//go:build integration

package brain

import (
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestLiveOllamaConversationGrounding(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_ROUTING_MODEL")
	}
	for _, tc := range []struct{ input, route string }{
		{"Find and attach a PNG of a dog.", "spawn"},
		{"Spin up a worker to research dogs on the web.", "spawn"},
		{"What is my name and what is yours?", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			root := t.TempDir()
			o, err := NewOllama(&config.Config{OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaThink: "false", OllamaNumCtx: 8192, AgentName: "Obey", UserDisplayName: "Lance", Persona: "obey", WorkDir: root, PromptsDir: root, MaxHistory: 10, VoiceToolsEnabled: true, VoiceRouterTools: "route_only"})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := o.ThinkStream(t.Context(), tc.input, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
			if err != nil {
				t.Fatal(err)
			}
			spoken, _ := drainStream(t, stream)
			result := <-stream.Done
			t.Logf("route=%q spoken=%q error=%v", result.Route, spoken, result.Err)
			if result.Err != nil || result.Route != tc.route {
				t.Fatalf("unexpected result %+v", result)
			}
			if tc.route != "" && spoken != "" {
				t.Fatal("routing preamble leaked")
			}
			if tc.route == "" && (!strings.Contains(spoken, "Lance") || !strings.Contains(spoken, "Obey")) {
				t.Fatal("identity answer lost user or assistant")
			}
			req := BuildCompletionRequest("", "", []Turn{{Role: "user", Content: "Spin up a worker to research dogs on the web."}}, []CompletionResult{{ResultID: "r1", JobID: "j1", Status: "succeeded", Brief: "Dogs were domesticated from wolves.", Limitations: "No image files were downloaded or attached."}})
			env, err := o.Complete(t.Context(), req)
			t.Logf("completion=%+v error=%v", env, err)
			if err != nil {
				t.Fatal(err)
			}
			if env.Answer != "" || len(env.Updates) == 0 {
				t.Fatal("completion did not report result update")
			}
			for _, bad := range []string{"I'll spin", "I will spin", "I'll start", "I will start", "I've attached"} {
				if strings.Contains(env.Render(), bad) {
					t.Fatalf("stale promise: %s", env.Render())
				}
			}
		})
	}
}
