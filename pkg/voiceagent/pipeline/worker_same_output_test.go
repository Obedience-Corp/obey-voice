package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

// rewordedListings each print the same listing, worded differently.
var rewordedListings = []string{
	"ls", "ls -1", "ls | cat", "ls -1 | cat", "ls .", "ls ./", "ls -- .", "command ls",
	"ls | sort", "ls | cat | cat", "ls -1 .", "ls | head -n 100", "ls -1 ./", "ls -1 -- .",
}

func listingCall(command string) api.ChatResponse {
	args := api.NewToolCallFunctionArguments()
	args.Set("command", command)
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command", Arguments: args}}}}}
}

func awaitExit(t *testing.T, table *job.Table, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := table.AwaitExit(ctx, id); err != nil {
		t.Fatalf("the worker never returned: %v", err)
	}
}

// A worker that lists one folder round after round, rewording the command,
// is never stopped for it. /workers and the persona's job list say how many
// rounds returned the same listing, the job keeps running, a cancel still
// ends it, and the count leaves with the job without reaching the saved
// conversation.
func TestRewordedListingJobShowsSameOutputAndStillCancels(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	root, workDir := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const pauseAt = 12
	paused := make(chan struct{})
	var mu sync.Mutex
	workerRequests := 0
	var liveRequests []api.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"},{"name":"fixture-worker"}]}`))
			return
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`))
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Model != "fixture-worker" {
			mu.Lock()
			liveRequests = append(liveRequests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: "It is still listing the folder."}})
			return
		}
		mu.Lock()
		workerRequests++
		n := workerRequests
		mu.Unlock()
		if len(req.Tools) == 0 {
			t.Errorf("worker request %d withheld tools", n)
		}
		if n >= pauseAt {
			if n == pauseAt {
				close(paused)
			}
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(listingCall(rewordedListings[(n-1)%len(rewordedListings)]))
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: workDir, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 100, OllamaNumCtx: 32768, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture-worker", VoiceToolsEnabled: true}
	workerCfg := cfg.Clone()
	workerCfg.IsWorker = true
	workerCfg.OllamaModel = "fixture-worker"
	worker, err := brain.NewOllama(workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	live, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: workDir, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 100, OllamaNumCtx: 32768})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Brain: live, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("conversation-1"); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "transcript.jsonl")
	log, err := transcript.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log.Attach(p.Events)
	table := job.NewTable(t.Context())
	t.Cleanup(table.Close)
	// Registered before the job starts: a failed assertion must not leave the
	// held request open until the test times out.
	t.Cleanup(table.AbortAll)
	a := NewAgency(p, table, worker, cfg, nil)
	a.Wire()
	aborted := make(chan events.JobAborted, 1)
	ended := make(chan string, 2)
	var counts []int
	events.Subscribe(p.Events, func(e events.WorkerSameOutput) {
		mu.Lock()
		counts = append(counts, e.Rounds)
		mu.Unlock()
	})
	events.Subscribe(p.Events, func(e events.JobAborted) { aborted <- e })
	events.Subscribe(p.Events, func(e events.JobFinished) { ended <- "finished" })
	events.Subscribe(p.Events, func(e events.JobFailed) { ended <- "failed: " + e.Err })
	metrics := newTurnMetrics()
	a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "List the files in the project folder"})

	select {
	case <-paused:
	case outcome := <-ended:
		t.Fatalf("the job ended on its own: %s", outcome)
	case <-time.After(10 * time.Second):
		t.Fatal("the job never reached its twelfth request")
	}
	const want = "same output for 11 rounds"
	workers := p.Workers()
	if len(workers) != 1 || workers[0].State != "running" || brain.SameOutputNote(workers[0].SameOutput) != want {
		t.Fatalf("/workers = %+v", workers)
	}
	id := workers[0].ID
	mu.Lock()
	reported := append([]int(nil), counts...)
	mu.Unlock()
	if len(reported) != 9 || reported[0] != 3 || reported[8] != 11 {
		t.Fatalf("count events = %v, want 3 through 11", reported)
	}

	// A live turn while the job runs: the persona is told, and nothing of it
	// is kept in the conversation.
	if err := p.RunTurnTextMode(t.Context(), "How is the listing going?"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sent := append([]api.ChatRequest(nil), liveRequests...)
	mu.Unlock()
	told := false
	for _, req := range sent {
		for _, m := range req.Messages {
			told = told || strings.Contains(m.Content, ` output="`+want+`"`)
		}
	}
	if !told {
		t.Fatalf("no live request told the persona about the count: %d requests", len(sent))
	}
	for _, turn := range p.Brain.History() {
		if strings.Contains(turn.Content, "same output") {
			t.Fatalf("the count was saved in the conversation: %+v", turn)
		}
	}

	if err := p.CancelWork(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case outcome := <-ended:
		t.Fatalf("cancel did not end the job as cancelled: %s", outcome)
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end the job")
	}
	awaitExit(t, table, id)
	a.noteSameOutput(id, 12)
	for _, info := range p.Workers() {
		if info.SameOutput != 0 {
			t.Fatalf("an ended job still shows its count: %+v", info)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "same output") || strings.Contains(string(saved), "same_output") {
		t.Fatalf("the count was recorded: %s", saved)
	}
}

// countingWorker reports a count, waits at a barrier, then reports another,
// standing in for a worker paused after its round finished.
type countingWorker struct {
	first, late    int
	reached        chan struct{}
	resume         chan struct{}
	ignoresCancel  bool
	returnedReport chan struct{}
}

func (w countingWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{"done"}}).ThinkStream(ctx, input, opts)
}

func (w countingWorker) ThinkFull(ctx context.Context, _ string, opts brain.StreamOptions) (string, error) {
	opts.OnSameOutput(w.first)
	close(w.reached)
	if w.ignoresCancel {
		<-w.resume
	} else {
		select {
		case <-w.resume:
		case <-ctx.Done():
		}
	}
	if w.late > 0 {
		opts.OnSameOutput(w.late)
	}
	if w.returnedReport != nil {
		close(w.returnedReport)
	}
	return "", ctx.Err()
}

func (countingWorker) ClearHistory()            {}
func (countingWorker) History() []brain.Turn    { return nil }
func (countingWorker) LoadHistory([]brain.Turn) {}

func newCountingHarness(t *testing.T, w countingWorker) *budgetHarness {
	t.Helper()
	h := newBudgetHarness(t)
	t.Cleanup(h.agency.jobs.AbortAll)
	h.agency.ReplaceWorker(w, &config.Config{}, nil)
	return h
}

func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

// A worker that reports its count after the job's end was handled cannot
// bring the count back.
func TestCountReportedAfterTheJobEndedIsDropped(t *testing.T) {
	w := countingWorker{first: 4, late: 5, reached: make(chan struct{}), resume: make(chan struct{}), ignoresCancel: true, returnedReport: make(chan struct{})}
	h := newCountingHarness(t, w)
	var reported []int
	var mu sync.Mutex
	events.Subscribe(h.agency.pipeline.Events, func(e events.WorkerSameOutput) {
		mu.Lock()
		reported = append(reported, e.Rounds)
		mu.Unlock()
	})
	h.spawn("list the files here")
	awaitClosed(t, w.reached, "the worker never reported its count")
	workers := h.agency.pipeline.Workers()
	if len(workers) != 1 || workers[0].SameOutput != 4 {
		t.Fatalf("/workers = %+v", workers)
	}
	id := workers[0].ID
	if err := h.agency.pipeline.CancelWork(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the job")
	}
	close(w.resume)
	awaitClosed(t, w.returnedReport, "the worker never reported again")
	awaitExit(t, h.agency.jobs, id)
	h.agency.mu.Lock()
	left := h.agency.sameOutputs[id].rounds
	h.agency.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if left != 0 || len(reported) != 1 || reported[0] != 4 {
		t.Fatalf("after the job ended: count=%d events=%v", left, reported)
	}
}

// A conversation cleared while its job runs no longer receives that job's
// count, so the persona's job list stops showing it. Which jobs are listed is
// unchanged.
func TestClearedConversationDropsTheJobCountFromJobFacts(t *testing.T) {
	w := countingWorker{first: 6, reached: make(chan struct{}), resume: make(chan struct{})}
	h := newCountingHarness(t, w)
	defer close(w.resume)
	p := h.agency.pipeline
	if err := p.BeginConversationIdentity("conversation-1"); err != nil {
		t.Fatal(err)
	}
	h.spawn("list the files here")
	awaitClosed(t, w.reached, "the worker never reported its count")
	facts := p.workerFacts(true)
	if len(facts) != 1 || facts[0].SameOutput != 6 {
		t.Fatalf("facts before the clear = %+v", facts)
	}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	facts = p.workerFacts(true)
	if len(facts) != 1 || facts[0].State != "running" || facts[0].SameOutput != 0 {
		t.Fatalf("facts after the clear = %+v", facts)
	}
}

// A story-writing job reports its count the same way as any other job.
func TestStoryWriterReportsSameOutput(t *testing.T) {
	w := countingWorker{first: 3, reached: make(chan struct{}), resume: make(chan struct{})}
	h := newCountingHarness(t, w)
	close(w.resume)
	var reported []events.WorkerSameOutput
	events.Subscribe(h.agency.pipeline.Events, func(e events.WorkerSameOutput) { reported = append(reported, e) })
	h.agency.narrator.cfg = &config.Config{WorkDir: t.TempDir(), VoiceToolsEnabled: true}
	ctx := job.WithID(t.Context(), "job-7")
	_, _ = h.agency.narrator.storyWriterRun(nil, nil)(ctx)
	if len(reported) != 1 || reported[0].JobID != "job-7" || reported[0].Rounds != 3 {
		t.Fatalf("story job count events = %+v", reported)
	}
}
