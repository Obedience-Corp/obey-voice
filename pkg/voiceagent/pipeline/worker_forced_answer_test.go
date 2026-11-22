package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/ollama/ollama/api"
)

type forcedJob struct {
	finished events.JobFinished
	saved    []brain.Turn
	record   map[string]any
}

func readCall(path string) api.ChatResponse {
	args := api.NewToolCallFunctionArguments()
	args.Set("path", path)
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: args}}}}}
}

func textReply(text string) api.ChatResponse {
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: text}}
}

// runOllamaJob spawns one job on an Ollama worker served by chat, delivers its
// answer to the saved conversation, and returns what each place recorded.
// chat receives the 1-based request number and the request.
func runOllamaJob(t *testing.T, budget time.Duration, chat func(n int, req api.ChatRequest) api.ChatResponse) forcedJob {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.png"), testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
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
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n > 40 {
			t.Error("unbounded worker requests")
			http.Error(w, "loop", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(chat(n, req))
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 100, OllamaNumCtx: 32768, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture", VoiceToolsEnabled: true}
	workerCfg := cfg.Clone()
	workerCfg.IsWorker = true
	worker, err := brain.NewOllama(workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	live, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: root, PromptsDir: filepath.Join(root, "prompts")})
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
	var opts []job.Option
	if budget > 0 {
		opts = append(opts, job.WithBudget(budget))
	}
	table := job.NewTable(t.Context(), opts...)
	t.Cleanup(table.Close)
	a := NewAgency(p, table, worker, cfg, nil)
	a.Wire()
	store := session.NewStore(filepath.Join(root, "sessions"))
	saved := session.New("ollama", "fixture")
	p.OnTurn = func() {
		if err := store.Save(saved, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	finished := make(chan events.JobFinished, 1)
	failed := make(chan events.JobFailed, 1)
	events.Subscribe(p.Events, func(e events.JobFinished) { finished <- e })
	events.Subscribe(p.Events, func(e events.JobFailed) { failed <- e })
	metrics := newTurnMetrics()
	a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Check the files in the fixture folder"})
	var out forcedJob
	select {
	case out.finished = <-finished:
	case e := <-failed:
		t.Fatalf("job failed: %s", e.Err)
	case <-time.After(10 * time.Second):
		t.Fatal("job did not finish")
	}
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	out.saved = loaded.Turns
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec map[string]any
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec["type"] == "job" && rec["state"] == "finished" {
			out.record = rec
		}
	}
	if out.record == nil {
		t.Fatal("no finished job record in the transcript")
	}
	return out
}

func checkCutShort(t *testing.T, got forcedJob, answer string, reason brain.ForcedAnswer) {
	t.Helper()
	if !got.finished.CutShort || got.finished.CutShortReason != string(reason) || got.finished.Summary != answer {
		t.Fatalf("job finished as %+v", got.finished)
	}
	if got.record["cut_short"] != true || got.record["reason"] != string(reason) {
		t.Fatalf("transcript record = %v", got.record)
	}
	if len(got.saved) != 1 || got.saved[0].Content != answer || got.saved[0].Disposition != brain.IncompleteDisposition(reason) {
		t.Fatalf("saved conversation = %+v", got.saved)
	}
}

func TestRepeatGuardAnswerIsDeliveredCutShort(t *testing.T) {
	const answer = "The file could not be read, so the check is blocked."
	got := runOllamaJob(t, 0, func(_ int, req api.ChatRequest) api.ChatResponse {
		if len(req.Tools) == 0 {
			return textReply(answer)
		}
		return readCall("missing.txt")
	})
	checkCutShort(t, got, answer, brain.ForcedByRepeat)
}

func TestTimeLimitAnswerIsDeliveredCutShort(t *testing.T) {
	const answer = "Two files were read; the rest remain."
	// A 4s limit keeps 2s for the answer, so the wrap-up comes after a few
	// slow tool rounds and well before the job is stopped.
	got := runOllamaJob(t, 4*time.Second, func(n int, req api.ChatRequest) api.ChatResponse {
		if len(req.Tools) == 0 {
			return textReply(answer)
		}
		time.Sleep(500 * time.Millisecond)
		return readCall("file-" + string(rune('a'+n)) + ".txt")
	})
	checkCutShort(t, got, answer, brain.ForcedByTime)
}

func TestFinishedJobIsNotCutShort(t *testing.T) {
	const answer = "Both files were checked and are fine."
	got := runOllamaJob(t, 0, func(n int, req api.ChatRequest) api.ChatResponse {
		if n == 1 && len(req.Tools) > 0 {
			return readCall("notes.txt")
		}
		return textReply(answer)
	})
	if got.finished.CutShort || got.finished.CutShortReason != "" || got.finished.Summary != answer {
		t.Fatalf("job finished as %+v", got.finished)
	}
	if _, ok := got.record["cut_short"]; ok {
		t.Fatalf("transcript record = %v", got.record)
	}
	if len(got.saved) != 1 || got.saved[0].Content != answer || got.saved[0].Disposition != "" {
		t.Fatalf("saved conversation = %+v", got.saved)
	}
}

// The image-repair request starts a fresh tool loop that finishes normally;
// the job must still say its first answer was forced.
func TestImageRepairAfterForcedAnswerKeepsItCutShort(t *testing.T) {
	const answer = "Here is the chart I found.\nChart"
	var forced atomic.Bool
	got := runOllamaJob(t, 0, func(_ int, req api.ChatRequest) api.ChatResponse {
		switch {
		case forced.Load():
			return textReply("Here is the chart I found.\n![Chart](fixture.png)")
		case len(req.Tools) == 0:
			forced.Store(true)
			return textReply("![Chart](https://example.invalid/chart)")
		}
		return readCall("missing.txt")
	})
	if !forced.Load() {
		t.Fatal("the fixture never forced an answer")
	}
	if len(got.finished.Attachments) != 1 {
		t.Fatalf("the repaired image was not attached: %+v", got.finished)
	}
	checkCutShort(t, got, answer, brain.ForcedByRepeat)
	if len(got.saved[0].Attachments) != 1 {
		t.Fatalf("saved reply lost its image: %+v", got.saved[0])
	}
}

// forcedWorker answers as a worker made to wrap up does.
type forcedWorker struct {
	answer string
	reason brain.ForcedAnswer
}

func (*forcedWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *forcedWorker) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	if w.reason != "" {
		opts.OnForcedAnswer(w.reason)
	}
	return w.answer, nil
}
func (*forcedWorker) ClearHistory()            {}
func (*forcedWorker) History() []brain.Turn    { return nil }
func (*forcedWorker) LoadHistory([]brain.Turn) {}

func TestWorkInboxResultOfForcedAnswerIsPartial(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reason      brain.ForcedAnswer
		status      work.OutcomeStatus
		limitations string
	}{
		{"forced", brain.ForcedByTime, work.OutcomePartial, "cut short: time limit nearly reached"},
		{"finished", "", work.OutcomeSucceeded, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := job.NewTable(context.Background())
			t.Cleanup(jobs.Close)
			worker := &forcedWorker{answer: "Two files were read; the rest remain.", reason: tc.reason}
			cfg := &config.Config{}
			a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, worker, cfg, nil)
			a.Wire()
			scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
			c := work.NewCoordinator(scope)
			if _, err := c.Register(scope, work.Admission{JobID: "job-1", Task: "read the files"}); err != nil {
				t.Fatal(err)
			}
			a.workMu.Lock()
			a.coordinator, a.workScope = c, scope
			a.jobScopes["job-1"] = scope
			a.workMu.Unlock()
			if _, err := jobs.Start(context.Background(), job.Spec{ID: "job-1", Task: "read the files", Run: a.workerRun("read the files", worker, cfg)}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(c.Pending(scope)) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("no outcome was published")
				}
				time.Sleep(5 * time.Millisecond)
			}
			outcome := c.Pending(scope)[0].Outcome
			if outcome.Status != tc.status || outcome.Limitations != tc.limitations || outcome.Text != worker.answer {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

// When a moved reply hands off and its worker is made to answer, only the
// worker's answer is cut short: the sentences kept before the move finished.
func TestMovedHandOffForcedAnswerLeavesTheKeptPartFinished(t *testing.T) {
	c := newMovingConversation(t)
	worker := &forcedWorker{answer: "Two files were read; the rest remain.", reason: brain.ForcedByRepeat}
	c.p.Agency.ReplaceWorker(worker, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker-model"}, nil)
	c.anika.route = "spawn"
	c.anika.before = []string{"Let me check the files. "}
	ended, obey, _ := endMovedReply(t, c)
	finished, ok := ended.(events.JobFinished)
	if !ok || !finished.CutShort || finished.CutShortReason != string(brain.ForcedByRepeat) {
		t.Fatalf("job ended as %+v", ended)
	}
	c.p.Agency.mu.Lock()
	keptID := c.p.Agency.workerOrigins[finished.ID].keptResponseID
	c.p.Agency.mu.Unlock()
	if keptID == "" {
		t.Fatal("nothing was kept before the move")
	}
	// The sentences given before the move, as the conversation recorded them.
	obey.LoadHistory(append(obey.History(), brain.Turn{Role: "assistant", Content: "Let me check the files.", ResponseID: keptID}))
	var kept, answer *brain.Turn
	for deadline := time.Now().Add(5 * time.Second); answer == nil && time.Now().Before(deadline); {
		if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, turn := range assistantTurns(obey.History()) {
			switch turn.Content {
			case "Let me check the files.":
				kept = &turn
			case worker.answer:
				answer = &turn
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if kept == nil || answer == nil || kept.Disposition != "" || answer.Disposition != brain.IncompleteDisposition(brain.ForcedByRepeat) {
		t.Fatalf("saved replies = %+v", assistantTurns(obey.History()))
	}
}
