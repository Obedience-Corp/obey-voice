package pipeline

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

const steerText = "Use the other file."

// steerJobHarness spawns jobs on an in-process Ollama worker served by a fake
// Ollama, and records every worker request and every correction event.
type steerJobHarness struct {
	t       *testing.T
	p       *Pipeline
	a       *Agency
	table   *job.Table
	root    string
	worker  brain.Provider
	workCfg *config.Config

	mu          sync.Mutex
	reqs        []api.ChatRequest
	corrections []events.JobCorrection
	reply       func(n int, req api.ChatRequest) (api.ChatResponse, int)

	finished chan events.JobFinished
	failed   chan events.JobFailed
	aborted  chan events.JobAborted
	queued   chan string
}

func newSteerJobHarness(t *testing.T, reply func(n int, req api.ChatRequest) (api.ChatResponse, int)) *steerJobHarness {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	h := &steerJobHarness{t: t, root: t.TempDir(), reply: reply,
		finished: make(chan events.JobFinished, 4), failed: make(chan events.JobFailed, 4),
		aborted: make(chan events.JobAborted, 4), queued: make(chan string, 4)}
	for _, name := range []string{"notes.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(h.root, name), []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
		h.mu.Lock()
		h.reqs = append(h.reqs, req)
		n := len(h.reqs)
		h.mu.Unlock()
		if n > 30 {
			t.Error("unbounded worker requests")
			http.Error(w, "loop", http.StatusInternalServerError)
			return
		}
		response, status := h.reply(n, req)
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"failed"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: h.root, PromptsDir: filepath.Join(h.root, "prompts"), MaxHistory: 100, OllamaNumCtx: 32768, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture", VoiceToolsEnabled: true}
	h.workCfg = cfg.Clone()
	h.workCfg.IsWorker = true
	worker, err := brain.NewOllama(h.workCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	h.worker = worker
	live, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: h.root, PromptsDir: filepath.Join(h.root, "prompts")})
	if err != nil {
		t.Fatal(err)
	}
	h.p = &Pipeline{Brain: live, Events: events.NewBus()}
	h.p.SetOutputMuted(true)
	h.table = job.NewTable(context.Background())
	t.Cleanup(h.table.Close)
	h.a = NewAgency(h.p, h.table, worker, cfg, nil)
	h.a.Wire()
	events.Subscribe(h.p.Events, func(e events.JobFinished) { h.finished <- e })
	events.Subscribe(h.p.Events, func(e events.JobFailed) { h.failed <- e })
	events.Subscribe(h.p.Events, func(e events.JobAborted) { h.aborted <- e })
	events.Subscribe(h.p.Events, func(e events.JobQueued) { h.queued <- e.ID })
	events.Subscribe(h.p.Events, func(e events.JobCorrection) {
		h.mu.Lock()
		h.corrections = append(h.corrections, e)
		h.mu.Unlock()
	})
	return h
}

func (h *steerJobHarness) spawn(task string) string {
	h.t.Helper()
	metrics := newTurnMetrics()
	h.a.HandleRoute(context.Background(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: task})
	select {
	case id := <-h.queued:
		return id
	case <-time.After(5 * time.Second):
		h.t.Fatal("job was not admitted")
		return ""
	}
}

func (h *steerJobHarness) awaitFinished(id string) events.JobFinished {
	h.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-h.finished:
			if e.ID == id {
				return e
			}
		case e := <-h.failed:
			h.t.Fatalf("job failed: %s", e.Err)
		case <-timeout:
			h.t.Fatal("job did not finish")
			return events.JobFinished{}
		}
	}
}

func (h *steerJobHarness) requests() []api.ChatRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.reqs)
}

func (h *steerJobHarness) events() []events.JobCorrection {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.corrections)
}

func (h *steerJobHarness) states() []string {
	var out []string
	for _, e := range h.events() {
		out = append(out, e.State)
	}
	return out
}

func userCount(req api.ChatRequest, text string) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "user" {
			n += strings.Count(m.Content, text)
		}
	}
	return n
}

func TestTellRunningJobReachesItsNextRequest(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			return readCall("notes.txt"), 0
		case 2:
			close(inFlight)
			<-release
			return textReply("notes.txt holds its contents."), 0
		}
		return textReply("Used other.txt instead."), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	receipt := h.p.TellWork(id, steerText)
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlAccepted || receipt.Jobs[0].Revision != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	close(release)
	finished := h.awaitFinished(id)
	if finished.Summary != "Used other.txt instead." || finished.CutShort {
		t.Fatalf("delivered %+v", finished)
	}
	reqs := h.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests=%d", len(reqs))
	}
	msgs := reqs[2].Messages
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != steerText {
		t.Fatalf("next request does not end with the correction: %+v", last)
	}
	if got := h.table.Status(); len(got) != 1 || got[0].State != job.StateDone {
		t.Fatalf("jobs=%+v", got)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "included"}) {
		t.Fatalf("correction events=%q", got)
	}
	info := onlyWorker(t, h.p)
	if info.Correction != "correction included" || info.State != string(job.StateDone) {
		t.Fatalf("workers row=%+v", info)
	}
	var users []string
	for _, turn := range info.Thread {
		if turn.Role == "user" {
			users = append(users, turn.Content)
		}
	}
	if len(users) != 2 || users[1] != steerText {
		t.Fatalf("saved thread user turns=%q", users)
	}
	if last := info.Thread[len(info.Thread)-1]; last.Role != "assistant" || last.Content != finished.Summary {
		t.Fatalf("saved thread ends %+v, delivered %q", last, finished.Summary)
	}
	if after := h.p.TellWork(id, "Another one."); after.Jobs[0].Outcome != ControlFinished {
		t.Fatalf("correction after finish: %+v", after)
	}
}

func TestTellQueuedJobAddsToItsBrief(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("Read other.txt."), 0
	})
	h.table.SetWorkerLimits(1, 4)
	hold := make(chan struct{})
	if _, err := h.table.Start(context.Background(), job.Spec{Task: "hold the slot", Run: func(context.Context) (string, error) { <-hold; return "", nil }}); err != nil {
		t.Fatal(err)
	}
	<-h.queued
	id := h.spawn("Read notes.txt in the fixture folder")
	receipt := h.p.TellWork(id, steerText)
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlBriefed {
		t.Fatalf("receipt=%+v", receipt)
	}
	close(hold)
	h.awaitFinished(id)
	first := h.requests()[0]
	if n := userCount(first, "Correction from the user: "+steerText); n != 1 {
		t.Fatalf("brief carries the correction %d times: %+v", n, first.Messages)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "included"}) || !h.events()[0].Brief {
		t.Fatalf("correction events=%+v", h.events())
	}
}

func TestTellRacingQueuePromotionAppliesOnce(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return readCall("notes.txt"), 0
		}
		return textReply("Read other.txt."), 0
	})
	h.table.SetWorkerLimits(1, 4)
	hold := make(chan struct{})
	if _, err := h.table.Start(context.Background(), job.Spec{Task: "hold the slot", Run: func(context.Context) (string, error) { <-hold; return "", nil }}); err != nil {
		t.Fatal(err)
	}
	<-h.queued
	id := h.spawn("Read notes.txt in the fixture folder")
	var receipt WorkReceipt
	var wg sync.WaitGroup
	wg.Go(func() { receipt = h.p.TellWork(id, steerText) })
	close(hold)
	wg.Wait()
	h.awaitFinished(id)
	if o := receipt.Jobs[0].Outcome; o != ControlBriefed && o != ControlAccepted {
		t.Fatalf("receipt=%+v", receipt)
	}
	reqs := h.requests()
	if n := userCount(reqs[len(reqs)-1], steerText); n != 1 {
		t.Fatalf("the last request carries the correction %d times", n)
	}
	if got := h.states(); got[len(got)-1] != "included" {
		t.Fatalf("correction events=%q", got)
	}
}

func TestFinalAnswerRaceNeverLosesACorrection(t *testing.T) {
	inFlight := make(chan struct{})
	var once sync.Once
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			once.Do(func() { close(inFlight) })
			return textReply("Finished the first plan."), 0
		}
		return textReply("Followed the correction."), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	// A random number of yields spreads the correction across the reply's
	// return, its import and the seal.
	for range rand.IntN(40000) {
		runtime.Gosched()
	}
	receipt := h.p.TellWork(id, steerText)
	finished := h.awaitFinished(id)
	reqs := h.requests()
	carried := userCount(reqs[len(reqs)-1], steerText)
	t.Logf("correction outcome: %s", receipt.Jobs[0].Outcome)
	switch receipt.Jobs[0].Outcome {
	case ControlAccepted:
		if carried != 1 || finished.Summary != "Followed the correction." {
			t.Fatalf("accepted but carried=%d delivered=%q", carried, finished.Summary)
		}
		if got := h.states(); !slices.Equal(got, []string{"pending", "included"}) {
			t.Fatalf("correction events=%q", got)
		}
	case ControlFinished:
		if carried != 0 || finished.Summary != "Finished the first plan." || len(h.events()) != 0 {
			t.Fatalf("refused but carried=%d delivered=%q events=%+v", carried, finished.Summary, h.events())
		}
	default:
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestCancelSupersedesAPendingCorrection(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		close(inFlight)
		<-release
		return textReply("Too late."), 0
	})
	defer close(release)
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("receipt=%+v", r)
	}
	if err := h.p.CancelWork(id); err != nil {
		t.Fatal(err)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "superseded"}) {
		t.Fatalf("correction events=%q", got)
	}
	if r := h.p.TellWork(id, "After the cancel."); r.Jobs[0].Outcome != ControlFinished {
		t.Fatalf("correction after cancel: %+v", r)
	}
}

func TestCorrectionDuringAttachmentImportContinuesTheJob(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return textReply("Here is the chart.\n![chart](missing-chart.png)"), 0
		}
		return textReply("Text only, as asked: notes.txt holds its contents."), 0
	})
	var once sync.Once
	var receipt WorkReceipt
	events.Subscribe(h.p.Events, func(e events.Info) {
		if strings.Contains(e.Message, "missing-chart.png") {
			once.Do(func() { receipt = h.p.TellWork("job-1", "Skip the chart; text only.") })
		}
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	finished := h.awaitFinished(id)
	if receipt.Jobs == nil || receipt.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("no correction during import: %+v", receipt)
	}
	reqs := h.requests()
	if len(reqs) != 2 || userCount(reqs[1], "Skip the chart; text only.") != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if finished.Summary != "Text only, as asked: notes.txt holds its contents." {
		t.Fatalf("delivered %q", finished.Summary)
	}
}

func TestNoImageRepairAfterAnIncludedCorrection(t *testing.T) {
	var a *Agency
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "No picture; describe it in text."})
			return readCall("notes.txt"), 0
		}
		return textReply("A described picture: a plain chart of the notes."), 0
	})
	a = h.a
	steer := a.newJobSteer()
	a.registerSteer("job-1", steer)
	ctx := withJobSteer(job.WithID(t.Context(), "job-1"), steer)
	ctx = context.WithValue(ctx, imageDeliveryKey{}, true)
	result, err := a.workerRunWithBudget("Send a picture of the notes.", h.worker, h.workCfg, nil, true)(ctx)
	if err != nil || result != "A described picture: a plain chart of the notes." {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("requests=%d: an image repair ran after the correction", n)
	}
}

func TestThreadCheckpointSurvivesAFailedRequest(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return readCall("notes.txt"), 0
		}
		return api.ChatResponse{}, http.StatusBadRequest
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	select {
	case <-h.failed:
	case <-time.After(10 * time.Second):
		t.Fatal("job did not fail")
	}
	info := onlyWorker(t, h.p)
	if info.ID != id || !slices.ContainsFunc(info.Thread, func(turn brain.Turn) bool { return turn.Role == "tool" }) {
		t.Fatalf("saved thread lost the completed tool step: %+v", info.Thread)
	}
}

func TestTellRejections(t *testing.T) {
	h := newControlHarness(t, stubbornHookWorker{release: make(chan struct{})}, false)
	h.spawn("list the files here")
	<-h.tool
	id := h.agency.jobs.Status()[0].ID
	cmd := WorkActor{Kind: WorkActorCommand}
	cases := []struct {
		name   string
		actor  WorkActor
		req    WorkRequest
		reject bool
		want   ControlOutcome
	}{
		{"worker without input", cmd, WorkRequest{Action: WorkSteer, JobID: id, Instruction: steerText}, false, ControlRejected},
		{"tool actor", WorkActor{Kind: WorkActorTool}, WorkRequest{Action: WorkSteer, JobID: id, Instruction: steerText}, true, ""},
		{"phrase actor", WorkActor{Kind: WorkActorPhrase}, WorkRequest{Action: WorkSteer, JobID: id, Instruction: steerText}, true, ""},
		{"empty instruction", cmd, WorkRequest{Action: WorkSteer, JobID: id, Instruction: "  "}, true, ""},
		{"no job", cmd, WorkRequest{Action: WorkSteer, Instruction: steerText}, true, ""},
		{"every job", cmd, WorkRequest{Action: WorkSteer, All: true, Instruction: steerText}, true, ""},
		{"unknown job", cmd, WorkRequest{Action: WorkSteer, JobID: "job-404", Instruction: steerText}, false, ControlUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.agency.ControlWork(c.actor, c.req)
			if c.reject {
				if r.Rejected == "" || len(r.Jobs) != 0 {
					t.Fatalf("receipt=%+v", r)
				}
				return
			}
			if len(r.Jobs) != 1 || r.Jobs[0].Outcome != c.want {
				t.Fatalf("receipt=%+v", r)
			}
			if c.want == ControlRejected && r.Jobs[0].Reason != steerNoInput {
				t.Fatalf("reason=%q", r.Jobs[0].Reason)
			}
		})
	}
	if got := h.agency.jobs.Status(); len(got) != 1 || got[0].State != job.StateRunning {
		t.Fatalf("a refused correction touched the job: %+v", got)
	}
}
