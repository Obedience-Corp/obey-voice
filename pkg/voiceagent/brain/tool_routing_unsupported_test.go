package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The live router must never retry a rejected route tool as ordinary chat:
// that converts a weather lookup into an ungrounded answer or capability denial.
func TestUnsupportedLiveRouterDoesNotFallBackToChat(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "streaming"}[streaming], func(t *testing.T) {
			var withTools, withoutTools int
			o := &OllamaBrain{
				client: ollamaStub(t, &withTools, &withoutTools), model: "chat-only",
				workDir: t.TempDir(), cfg: &config.Config{PromptsDir: t.TempDir(), MaxHistory: 10},
			}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			var answer string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Spin up a worker to find the weather in Denver", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				answer, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				answer, err = o.ThinkFull(context.Background(), "Spin up a worker to find the weather in Denver", opts)
			}
			if err == nil || !strings.Contains(err.Error(), "chat-only") || !strings.Contains(err.Error(), "Settings") {
				t.Fatalf("missing actionable model compatibility error: %v", err)
			}
			if answer != "" || withTools != 1 || withoutTools != 0 {
				t.Fatalf("unsupported routing became chat: answer=%q tool requests=%d chat requests=%d", answer, withTools, withoutTools)
			}
			for _, turn := range o.History() {
				if turn.Role == "assistant" {
					t.Fatalf("persisted fabricated reply after routing failed: %q", turn.Content)
				}
			}
		})
	}
}
