package voiceagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// idleJobWorker runs a job until it is cancelled.
type idleJobWorker struct{}

func (idleJobWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, context.Canceled
}

func (idleJobWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (idleJobWorker) ClearHistory()            {}
func (idleJobWorker) History() []brain.Turn    { return nil }
func (idleJobWorker) LoadHistory([]brain.Turn) {}

// controlAgent is an Agent whose live persona is Ollama on the direct router
// with job control on, built as an embedding host builds one: no conversation
// identity and no persona runtime. The fake model hands "Work on this in the
// background." to a worker and answers "cancel that job" with a cancel of
// job-1.
func controlAgent(t *testing.T) (*Agent, func() []events.RouteDecided) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
			return
		case "/api/chat":
		default:
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
			return
		}
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		human := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				human, _, _ = strings.Cut(req.Messages[i].Content, "\n")
				break
			}
		}
		message := map[string]any{"role": "assistant", "content": "Okay."}
		call := func(name string, args map[string]string) {
			message = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "call-1", "function": map[string]any{"name": name, "arguments": args}}}}
		}
		switch {
		case strings.Contains(human, "Work on this in the background."):
			call("start_background_work", map[string]string{})
		case strings.Contains(human, "cancel that job"):
			call("job_control", map[string]string{"action": "cancel", "job_id": "job-1"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "done": true, "done_reason": "stop", "message": message})
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{AgentName: "Robin", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture",
		PromptsDir: t.TempDir(), WorkDir: t.TempDir(), MaxHistory: 40, VoiceToolsEnabled: true, VoiceRouterTools: "legacy", VoiceJobControl: true}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var mu sync.Mutex
	var settled []events.RouteDecided
	events.Subscribe(bus, func(e events.RouteDecided) {
		if e.Kind == "job.control" && e.State != "handled" {
			mu.Lock()
			settled = append(settled, e)
			mu.Unlock()
		}
	})
	agent, cleanup, err := New(context.Background(), Options{Config: cfg, Events: bus, Brain: live, Worker: idleJobWorker{}, TextOnly: true, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return agent, func() []events.RouteDecided {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.RouteDecided(nil), settled...)
	}
}

func (a *Agent) workerState(id string) string {
	for _, w := range a.Pipeline.Workers() {
		if w.ID == id {
			return w.State
		}
	}
	return ""
}

// A host that never starts a conversation identity, as an embedder or the
// convoflow harness, still lets the persona cancel the job it handed off.
func TestAgentPersonaCancelsItsJobWithoutAConversationIdentity(t *testing.T) {
	agent, settled := controlAgent(t)
	if err := agent.SendText(t.Context(), "Work on this in the background."); err != nil {
		t.Fatal(err)
	}
	if state := agent.workerState("job-1"); state != "running" {
		t.Fatalf("job-1 is %q", state)
	}
	if err := agent.SendText(t.Context(), "cancel that job"); err != nil {
		t.Fatal(err)
	}
	got := settled()
	if len(got) != 1 || got[0].State != "applied" || agent.workerState("job-1") != "aborted" {
		t.Fatalf("settled %+v, job-1 %q", got, agent.workerState("job-1"))
	}
}

// Without a conversation identity a clear still ends the persona's hold on
// the jobs started before it.
func TestAgentClearEndsThePersonasHoldOnEarlierJobs(t *testing.T) {
	agent, settled := controlAgent(t)
	if err := agent.SendText(t.Context(), "Work on this in the background."); err != nil {
		t.Fatal(err)
	}
	if err := agent.Pipeline.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if err := agent.SendText(t.Context(), "cancel that job"); err != nil {
		t.Fatal(err)
	}
	got := settled()
	if len(got) != 1 || got[0].State != "refused" || agent.workerState("job-1") != "running" {
		t.Fatalf("settled %+v, job-1 %q", got, agent.workerState("job-1"))
	}
}
