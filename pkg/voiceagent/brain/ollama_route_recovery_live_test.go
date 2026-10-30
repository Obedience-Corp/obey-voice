//go:build integration

package brain

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// Local inference only: no worker, tool execution, audio or personal history.
func TestLiveMissingRouteRecovery(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_ROUTE_RECOVERY_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_ROUTE_RECOVERY_MODEL to an installed Ollama model")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434"
	}
	base, err := url.Parse(host)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: model, cfg: &config.Config{OllamaNumCtx: 8192, PromptsDir: t.TempDir()}}
	for _, tc := range []struct{ input, want string }{
		{"Nothing's wrong with me. You're the one with the ridiculous name.", "chat"},
		{"What's the weather in Denver today?", "spawn"},
		{"Read the project's README file and tell me how to run its tests.", "spawn"},
	} {
		t.Run(tc.want+"/"+tc.input, func(t *testing.T) {
			req := o.newChatRequest([]api.Message{{Role: "assistant", Content: "What's wrong with you, Lance?"}, {Role: "user", Content: tc.input}}, liveRouterTools())
			var response api.ChatResponse
			if err := o.chatRouteRecovery(t.Context(), req, func(r api.ChatResponse) error { response = r; return nil }); err != nil {
				t.Fatal(err)
			}
			if len(response.Message.ToolCalls) != 1 || toolCallArg(response.Message.ToolCalls[0], "kind") != tc.want {
				t.Fatalf("decision=%+v, want %s", response.Message, tc.want)
			}
		})
	}
}
