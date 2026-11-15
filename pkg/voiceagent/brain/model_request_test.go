package brain

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// The skill check runs before the user turn is committed, so it must still
// reach the observer with the caller's turn identity.
func TestSkillCheckIsObservedWithTheTurnContext(t *testing.T) {
	var chats []api.ChatRequest
	o := &OllamaBrain{client: recordingSelectionServerWith(t, &chats, `{"relevant":true,"names":["greeter"]}`), skillRouter: selectionTestRouter(t, "body"), model: "m", workDir: t.TempDir(),
		cfg: &config.Config{OllamaNumCtx: 8192, MaxHistory: 10, PromptsDir: t.TempDir()}}
	var mu sync.Mutex
	var seen []string
	o.SetModelRequestObserver(func(ctx context.Context, req ModelRequest) func(ModelRequestResult) {
		turn := "none"
		if id := events.TraceFromContext(ctx).TurnID; id != nil {
			turn = *id
		}
		mu.Lock()
		seen = append(seen, req.Purpose+"@"+turn)
		mu.Unlock()
		return func(ModelRequestResult) {}
	})
	turn := "turn-1"
	ctx := events.WithTrace(t.Context(), events.TraceContext{RuntimeID: "runtime", TurnID: &turn})
	stream, err := o.ThinkStream(ctx, "hello", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(seen, " "); got != "skill_check@turn-1 route@turn-1 reply@turn-1" {
		t.Fatalf("observed requests = %s", got)
	}
}
