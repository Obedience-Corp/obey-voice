//go:build integration

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// The worker result is an explicit fixture; this proves terminal admission and
// handoff with the real harness router, not current weather or a live search.
type routingProofWorker struct{ requests chan string }

func (*routingProofWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("worker is nonstreaming")
}
func (w *routingProofWorker) ThinkFull(_ context.Context, text string, opts brain.StreamOptions) (string, error) {
	if !opts.ToolsEnabled || opts.RouterToolsPolicy != brain.RouterToolsLegacy {
		return "", fmt.Errorf("worker lost execution policy")
	}
	w.requests <- text
	return "Recorded forecast fixture. Source: https://example.com/forecast", nil
}
func (*routingProofWorker) ClearHistory()            {}
func (*routingProofWorker) History() []brain.Turn    { return nil }
func (*routingProofWorker) LoadHistory([]brain.Turn) {}

func TestLiveHarnessRoutesTerminalRequest(t *testing.T) {
	provider := os.Getenv("OBEY_VOICE_LIVE_HARNESS_PROVIDER")
	if provider == "" {
		t.Skip("set OBEY_VOICE_LIVE_HARNESS_PROVIDER to an authenticated harness")
	}
	model := os.Getenv("OBEY_VOICE_LIVE_HARNESS_MODEL")
	cfg := &config.Config{BrainProvider: provider, Persona: "obey", AgentName: "Obey", WorkDir: t.TempDir(), PromptsDir: t.TempDir(), MaxHistory: 20,
		VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama", CodexModel: model, CodexReasoningEffort: "low", CursorModel: model, GrokModel: model,
		MuseSparkModel: model, ObeyModel: model, ObeyToolPolicy: "daemon", PersonaRoutingTimeout: 60}
	live, err := brain.NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.CloseProvider(live) }()
	live.LoadHistory([]brain.Turn{{Role: "user", Content: "Use Denver, Colorado for location."}})
	p := &pipeline.Pipeline{Brain: live, Events: events.NewBus(), VoiceToolsEnabled: true}
	p.SetOutputMuted(true)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	worker := &routingProofWorker{requests: make(chan string, 2)}
	pipeline.NewAgency(p, jobs, worker, cfg, nil).Wire()
	store := session.NewStore(t.TempDir())
	saved := session.New(provider, model)
	p.OnTurn = func() {
		if err := store.Save(saved, live.History()); err != nil {
			t.Error(err)
		}
	}
	result := make(chan events.JobFinished, 2)
	events.Subscribe(p.Events, func(e events.JobFinished) { result <- e })
	replies := make(chan string, 4)
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		if e.Response != "" {
			replies <- e.Response
		}
	})
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	appDone := make(chan error, 1)
	go func() { appDone <- Run(ctx, p, input, true, true) }()
	const query = "Can you tell me what the weather is today?"
	if _, err := fmt.Fprintln(write, query); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-result:
		if !strings.Contains(e.Summary, "https://example.com/forecast") {
			t.Fatal(e)
		}
		t.Logf("terminal worker result: %s", e.Summary)
	case <-ctx.Done():
		t.Fatal("terminal input did not reach a worker")
	}
	if _, err := fmt.Fprintln(write, "Can this app search the web?"); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		text := strings.ToLower(reply)
		if !strings.Contains(text, "web") || !strings.Contains(text, "worker") || strings.Contains(text, "no web access") || strings.Contains(text, "no web search tool connected") {
			t.Fatalf("incorrect capability reply: %s", reply)
		}
		t.Logf("terminal capability reply: %s", reply)
	case <-ctx.Done():
		t.Fatal("no capability reply")
	}
	if _, err := fmt.Fprintln(write, "quit"); err != nil {
		t.Fatal(err)
	}
	if err := <-appDone; err != nil {
		t.Fatal(err)
	}
	select {
	case brief := <-worker.requests:
		if !strings.Contains(brief, "Denver, Colorado") || !strings.Contains(brief, query) {
			t.Fatalf("lost constraints: %s", brief)
		}
	default:
		t.Fatal("missing worker request")
	}
	if len(jobs.Status()) != 1 {
		t.Fatal("duplicate job admission")
	}
	loaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, turn := range loaded.Turns {
		if turn.Role == "user" && turn.Content == query {
			found = true
		}
		if strings.Contains(turn.Content, `"kind":"spawn"`) {
			t.Fatal("private classifier decision persisted in conversation")
		}
	}
	if !found {
		t.Fatal("admitted terminal request was not persisted")
	}
}
