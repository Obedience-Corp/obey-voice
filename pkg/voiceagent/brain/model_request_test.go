package brain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
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

func observedStub(t *testing.T, frames ...string) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n")
		}
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	return api.NewClient(base, http.DefaultClient)
}

func recordResults(o *OllamaBrain) *[]ModelRequestResult {
	var results []ModelRequestResult
	o.SetModelRequestObserver(func(context.Context, ModelRequest) func(ModelRequestResult) {
		return func(r ModelRequestResult) { results = append(results, r) }
	})
	return &results
}

// A stream that closes without a done frame is a clean return with nothing
// measured by the provider.
func TestModelRequestWithoutDoneFrameHasNoProviderMetrics(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","content":"partial"}}`), model: "m"}
	results := recordResults(o)
	if err := o.clientChat(t.Context(), &api.ChatRequest{Model: "m"}, func(api.ChatResponse) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(*results) != 1 {
		t.Fatalf("finishes = %d", len(*results))
	}
	r := (*results)[0]
	if r.Err != nil || r.FirstChunk.IsZero() || r.PromptTokens != nil || r.OutputTokens != nil || r.Load != nil || r.Prefill != nil || r.Generation != nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestModelRequestEndsWhenTheCallbackPanics(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","content":"x"},"done":true}`), model: "m"}
	results := recordResults(o)
	type sentinel struct{}
	func() {
		defer func() {
			if r := recover(); r != (sentinel{}) {
				t.Fatalf("panic value = %v", r)
			}
		}()
		_ = o.clientChat(t.Context(), &api.ChatRequest{Model: "m"}, func(api.ChatResponse) error { panic(sentinel{}) })
	}()
	if len(*results) != 1 || (*results)[0].Err == nil {
		t.Fatalf("finishes = %+v", *results)
	}
}

// An invalid private selection is the engine stopping the request, reported as
// an error rather than a cancellation.
func TestInvalidSkillSelectionEndsAsAnError(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{}}}]},"done":true}`), model: "m", cfg: &config.Config{PromptsDir: t.TempDir()}}
	results := recordResults(o)
	if _, err := o.confirmSkillMatches(t.Context(), "hello", []skills.Skill{{Name: "greeter", Description: "Greet people"}}); err == nil {
		t.Fatal("invalid selection accepted")
	}
	if len(*results) != 1 {
		t.Fatalf("finishes = %d", len(*results))
	}
	if err := (*results)[0].Err; err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("selection stop reported as %v", err)
	}
}

func TestObserverWithoutFinishDoesNotBreakTheRequest(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","content":"x"},"done":true}`), model: "m"}
	o.SetModelRequestObserver(func(context.Context, ModelRequest) func(ModelRequestResult) { return nil })
	if err := o.clientChat(t.Context(), &api.ChatRequest{Model: "m"}, func(api.ChatResponse) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Background jobs run on fresh worker instances, which are never observed:
// their requests stay out of the conversation's model_request spans.
func TestFreshWorkerInstanceIsNotObserved(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","content":"x"},"done":true}`), model: "m"}
	results := recordResults(o)
	fresh := o.Fresh().(*OllamaBrain)
	if err := fresh.clientChat(t.Context(), &api.ChatRequest{Model: "m"}, func(api.ChatResponse) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(*results) != 0 {
		t.Fatalf("fresh instance reported %d requests", len(*results))
	}
}

func TestContinuationCompactionIsReportedAsCompaction(t *testing.T) {
	o := &OllamaBrain{client: observedStub(t, `{"message":{"role":"assistant","content":"A short summary."},"done":true,"done_reason":"stop"}`), model: "m", cfg: &config.Config{PromptsDir: t.TempDir(), MaxHistory: 10}}
	o.history = []api.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}
	var purposes []string
	o.SetModelRequestObserver(func(_ context.Context, req ModelRequest) func(ModelRequestResult) {
		purposes = append(purposes, req.Purpose)
		return nil
	})
	if err := o.compactForContinuation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(purposes, " ") != PurposeCompaction {
		t.Fatalf("purposes = %v", purposes)
	}
}
