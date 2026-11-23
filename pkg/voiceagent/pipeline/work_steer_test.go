package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
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
	shows       int
	corrections []events.JobCorrection
	reply       func(n int, req api.ChatRequest) (api.ChatResponse, int)

	finished chan events.JobFinished
	failed   chan events.JobFailed
	aborted  chan events.JobAborted
	queued   chan string
	accepted chan string
	snapshot chan work.Snapshot

	// jobID is the spawned job's id, for a reply that must name it.
	jobID   string
	idReady chan struct{}
}

// steerSetup selects how the harness admits jobs.
type steerSetup struct {
	// tieredRequests > 0 runs tiered workers on the local Ollama worker,
	// each job allowed that many model requests.
	tieredRequests int
	// inbox turns the work inbox on.
	inbox bool
	// budget is the job table's runtime budget; zero is none.
	budget time.Duration
	// workerProvider, when set, is the configured legacy worker provider,
	// which a tiered or own job does not run on.
	workerProvider string
}

func newSteerJobHarness(t *testing.T, reply func(n int, req api.ChatRequest) (api.ChatResponse, int)) *steerJobHarness {
	return newSteerJobHarnessWith(t, steerSetup{}, reply)
}

func newSteerJobHarnessWith(t *testing.T, setup steerSetup, reply func(n int, req api.ChatRequest) (api.ChatResponse, int)) *steerJobHarness {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	h := &steerJobHarness{t: t, root: t.TempDir(), reply: reply,
		finished: make(chan events.JobFinished, 4), failed: make(chan events.JobFailed, 4),
		aborted: make(chan events.JobAborted, 4), queued: make(chan string, 4), accepted: make(chan string, 4),
		snapshot: make(chan work.Snapshot, 64), idReady: make(chan struct{})}
	for _, name := range []string{"notes.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(h.root, name), []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"},{"name":"fixture:cloud"}]}`))
			return
		case "/api/show":
			h.mu.Lock()
			h.shows++
			h.mu.Unlock()
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
	if setup.workerProvider != "" {
		cfg.VoiceWorkerProvider = setup.workerProvider
	}
	if setup.tieredRequests > 0 {
		w := config.DefaultVoiceWorkers()
		w.Mode, w.Revision = config.WorkerModeTiered, 1
		host := server.URL
		w.Tiers.Simple.Enabled, w.Tiers.Simple.Revision = true, 1
		w.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "fixture", EndpointRef: &host, Billing: config.WorkerBillingLocal}
		w.Tiers.Simple.Limits.ModelRequests = setup.tieredRequests
		cfg.VoiceWorkers = w
	}
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
	var tableOpts []job.Option
	if setup.budget > 0 {
		tableOpts = append(tableOpts, job.WithBudget(setup.budget))
	}
	h.table = job.NewTable(context.Background(), tableOpts...)
	t.Cleanup(h.table.Close)
	h.a = NewAgency(h.p, h.table, worker, cfg, nil)
	h.a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) { return worker, nil }
	h.a.Wire()
	if setup.inbox {
		if err := h.p.EnableConversationWork("steer", nil, cfg); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.p.CloseWork() })
	}
	events.Subscribe(h.p.Events, func(e events.JobFinished) { h.finished <- e })
	events.Subscribe(h.p.Events, func(e events.JobFailed) { h.failed <- e })
	events.Subscribe(h.p.Events, func(e events.JobAborted) { h.aborted <- e })
	events.Subscribe(h.p.Events, func(e events.JobQueued) { h.queued <- e.ID })
	events.Subscribe(h.p.Events, func(e events.RouteDecided) {
		if e.State == "accepted" {
			h.accepted <- e.JobID
		}
	})
	events.Subscribe(h.p.Events, func(e events.WorkStateChanged) {
		select {
		case h.snapshot <- e.Snapshot:
		default:
		}
	})
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
	d := RouteDecision{Kind: RouteSpawn, Transcript: task}
	if h.a.cfg != nil && h.a.cfg.VoiceWorkers != nil {
		d.Tier = config.WorkerTierSimple
	}
	h.a.HandleRoute(context.Background(), h.p.newTurnConductor(metrics), metrics, d)
	select {
	case id := <-h.accepted:
		return id
	case <-time.After(5 * time.Second):
		h.t.Fatal("job was not admitted")
		return ""
	}
}

// setAccepted names the spawned job for replies waiting on lastAccepted.
func (h *steerJobHarness) setAccepted(id string) {
	h.jobID = id
	close(h.idReady)
}

func (h *steerJobHarness) lastAccepted() string {
	<-h.idReady
	return h.jobID
}

// awaitOutcome waits for the work inbox to publish job id's outcome.
func (h *steerJobHarness) awaitOutcome(id string) work.Outcome {
	h.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		for _, j := range h.p.WorkSnapshot().Jobs {
			if j.Key.JobID == id && j.Outcome != nil {
				return *j.Outcome
			}
		}
		select {
		case <-h.snapshot:
		case <-timeout:
			h.t.Fatal("no outcome was published")
			return work.Outcome{}
		}
	}
}

// steerState is where job id's newest correction stands.
func (h *steerJobHarness) steerState(id string) brain.CorrectionState {
	all := h.a.steerFor(id).control.Corrections()
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1].State
}

// holdSlot fills the only worker slot until the returned func is called.
func (h *steerJobHarness) holdSlot() func() {
	h.t.Helper()
	h.table.SetWorkerLimits(1, 4)
	hold := make(chan struct{})
	if _, err := h.table.Start(context.Background(), job.Spec{Task: "hold the slot", Run: func(context.Context) (string, error) { <-hold; return "", nil }}); err != nil {
		h.t.Fatal(err)
	}
	return func() { close(hold) }
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

func threadUsers(info WorkerInfo) []string {
	var users []string
	for _, turn := range info.Thread {
		if turn.Role == "user" {
			users = append(users, turn.Content)
		}
	}
	return users
}

// The correction arrives while the worker's first request is in flight,
// after it took its brief: it is accepted, and the answer that request
// returns is replaced by one written after the correction.
func TestTellAfterTheWorkerStartsReachesItsNextRequest(t *testing.T) {
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
	if len(reqs) != 3 || userCount(reqs[1], steerText) != 0 {
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
	if users := threadUsers(info); len(users) != 2 || users[1] != steerText {
		t.Fatalf("saved thread user turns=%q", users)
	}
	if last := info.Thread[len(info.Thread)-1]; last.Role != "assistant" || last.Content != finished.Summary {
		t.Fatalf("saved thread ends %+v, delivered %q", last, finished.Summary)
	}
	replaced := slices.IndexFunc(info.Thread, func(turn brain.Turn) bool { return turn.Disposition == brain.DispositionSuperseded })
	if replaced < 0 || info.Thread[replaced].Content != "notes.txt holds its contents." {
		t.Fatalf("the replaced answer is not marked in the thread: %+v", info.Thread)
	}
}

// The correction arrives while the job waits for a slot, before the worker
// takes its brief: it is in the brief once, and included once the first
// request is answered.
func TestTellBeforeTheWorkerStartsIsInItsBrief(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("Read other.txt."), 0
	})
	release := h.holdSlot()
	<-h.queued
	id := h.spawn("Read notes.txt in the fixture folder")
	receipt := h.p.TellWork(id, steerText)
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlBriefed {
		t.Fatalf("receipt=%+v", receipt)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending"}) || !h.events()[0].Brief {
		t.Fatalf("events before the start=%+v", h.events())
	}
	release()
	finished := h.awaitFinished(id)
	reqs := h.requests()
	if len(reqs) != 1 || userCount(reqs[0], "Correction from the user: "+steerText) != 1 {
		t.Fatalf("requests=%d brief=%+v", len(reqs), reqs[0].Messages)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "included"}) {
		t.Fatalf("correction events=%+v", h.events())
	}
	if finished.Summary != "Read other.txt." {
		t.Fatalf("delivered %q", finished.Summary)
	}
}

// A correction that waits while the reply's images are imported, before the
// answer is sealed, continues the job.
func TestCorrectionBeforeTheSealContinuesTheJob(t *testing.T) {
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
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("no correction during import: %+v", receipt)
	}
	reqs := h.requests()
	if len(reqs) != 2 || userCount(reqs[1], "Skip the chart; text only.") != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "included"}) {
		t.Fatalf("correction events=%q", got)
	}
	if finished.Summary != "Text only, as asked: notes.txt holds its contents." || finished.CutShort {
		t.Fatalf("delivered %+v", finished)
	}
}

// After the answer is sealed a correction is refused and changes nothing.
func TestCorrectionAfterTheSealIsRefused(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("Finished the first plan."), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	finished := h.awaitFinished(id)
	receipt := h.p.TellWork(id, steerText)
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlFinished {
		t.Fatalf("receipt=%+v", receipt)
	}
	if len(h.requests()) != 1 || len(h.events()) != 0 || finished.Summary != "Finished the first plan." {
		t.Fatalf("requests=%d events=%+v delivered=%q", len(h.requests()), h.events(), finished.Summary)
	}
}

func TestCancelSupersedesCorrections(t *testing.T) {
	t.Run("running job", func(t *testing.T) {
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
	})
	t.Run("queued job that never starts", func(t *testing.T) {
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			t.Error("a cancelled queued job ran")
			return textReply("ran"), 0
		})
		release := h.holdSlot()
		defer release()
		<-h.queued
		id := h.spawn("Read notes.txt in the fixture folder")
		if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlBriefed {
			t.Fatalf("receipt=%+v", r)
		}
		if err := h.p.CancelWork(id); err != nil {
			t.Fatal(err)
		}
		if got := h.states(); !slices.Equal(got, []string{"pending", "superseded"}) {
			t.Fatalf("correction events=%q", got)
		}
		if r := h.p.TellWork(id, "After the cancel."); r.Jobs[0].Outcome != ControlFinished {
			t.Fatalf("correction to a cancelled queued job: %+v", r)
		}
	})
	t.Run("cancel all", func(t *testing.T) {
		inFlight, release := make(chan struct{}), make(chan struct{})
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			close(inFlight)
			<-release
			return textReply("Too late."), 0
		})
		defer close(release)
		h.table.SetWorkerLimits(1, 4)
		running := h.spawn("Read notes.txt in the fixture folder")
		<-inFlight
		queued := h.spawn("Read other.txt in the fixture folder")
		h.p.TellWork(running, "First correction.")
		h.p.TellWork(queued, "Second correction.")
		if err := h.p.StopWorkers(); err != nil {
			t.Fatal(err)
		}
		final := map[string]string{}
		for _, e := range h.events() {
			final[e.ID] = e.State
		}
		if final[running] != "superseded" || final[queued] != "superseded" {
			t.Fatalf("dispositions=%v", final)
		}
	})
}

// A correction that drops the picture costs the one repair, which carries it,
// and the answer is then delivered saying no image was attached.
func TestTextOnlyCorrectionGetsOneRepairThenSaysNoImage(t *testing.T) {
	var a *Agency
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "No picture; describe it in text."})
			return readCall("notes.txt"), 0
		}
		return textReply("A described picture: a plain chart of the notes."), 0
	})
	a = h.a
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	result, err := h.directSteerRun(ctx, "Send a picture of the notes.", nil)
	if err != nil || result != "A described picture: a plain chart of the notes.\n\n"+noImageMarker {
		t.Fatalf("result=%q err=%v", result, err)
	}
	reqs := h.requests()
	if len(reqs) != 3 || userCount(reqs[2], "Correction from the user: No picture; describe it in text.") == 0 {
		t.Fatalf("requests=%d: the one repair did not carry the correction", len(reqs))
	}
	h.a.mu.Lock()
	noImage := h.a.workerOrigins["job-1"].noImage
	h.a.mu.Unlock()
	if !noImage {
		t.Fatal("the job does not record that no image was attached")
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

func TestCloudAliasWorkerRefusesCorrections(t *testing.T) {
	var a *Agency
	var receipt WorkReceipt
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		receipt = a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: steerText})
		return textReply("Done."), 0
	})
	a = h.a
	cloudCfg := h.workCfg.Clone()
	cloudCfg.OllamaModel = "fixture:cloud"
	cloud, err := brain.NewOllama(cloudCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cloud.Close() })
	h.worker = cloud
	if _, err := h.directSteerRun(t.Context(), "Read notes.txt.", nil); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlRejected || receipt.Jobs[0].Reason != steerRemote {
		t.Fatalf("receipt=%+v", receipt)
	}
}

// directSteerRun runs the worker run for job-1 directly, with corrections
// reachable as on a spawned job.
func (h *steerJobHarness) directSteerRun(ctx context.Context, brief string, budget *brain.WorkerBudget) (string, error) {
	steer := h.a.newJobSteer(work.WorkScope{})
	h.a.registerSteer("job-1", steer)
	ctx = withJobSteer(job.WithID(ctx, "job-1"), steer)
	return h.a.workerRunWithBudget(brief, h.worker, h.workCfg, budget, true)(ctx)
}

// Four requests: three identical tool rounds trip the repeat guard, and a
// correction arrives while the forced answer is written. No request is left
// for it, so that answer is delivered cut short rather than discarded.
func TestRepeatForcedAnswerStandsWhenACorrectionHasNoRequest(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "inbox"}[inbox], func(t *testing.T) {
			var h *steerJobHarness
			var receipt WorkReceipt
			h = newSteerJobHarnessWith(t, steerSetup{tieredRequests: 4, inbox: inbox}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
				if n <= 3 {
					return readCall("notes.txt"), 0
				}
				receipt = h.p.TellWork(h.lastAccepted(), steerText)
				return textReply("notes.txt kept returning the same lines."), 0
			})
			id := h.spawn("Read notes.txt in the fixture folder")
			h.setAccepted(id)
			if inbox {
				outcome := h.awaitOutcome(id)
				if outcome.Status != work.OutcomePartial || outcome.Limitations != "cut short: repeated the same tool call" || outcome.Text != "notes.txt kept returning the same lines." {
					t.Fatalf("outcome=%+v", outcome)
				}
			} else {
				finished := h.awaitFinished(id)
				if !finished.CutShort || finished.CutShortReason != string(brain.ForcedByRepeat) || finished.Summary != "notes.txt kept returning the same lines." {
					t.Fatalf("delivered %+v", finished)
				}
			}
			if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlAccepted {
				t.Fatalf("receipt=%+v", receipt)
			}
			if n := len(h.requests()); n != 4 {
				t.Fatalf("requests=%d", n)
			}
			got := h.events()
			if last := got[len(got)-1]; last.State != "not_applied" || last.Reason != brain.NotAppliedLimit {
				t.Fatalf("correction events=%+v", got)
			}
		})
	}
}

// A repeat-forced answer misses its image; the repair takes a correction and
// answers normally. The delivered answer is not marked by the first reason.
func TestCorrectionDuringImageRepairDecidesTheReason(t *testing.T) {
	var a *Agency
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch {
		case n <= 3:
			return readCall("notes.txt"), 0
		case n == 4:
			return textReply("I could not find a chart to attach."), 0
		case n == 5:
			a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Skip the picture; text only."})
			return readCall("notes.txt"), 0
		}
		return textReply("Text only: notes.txt lists three action items."), 0
	})
	a = h.a
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	result, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10}))
	if err != nil || result != "Text only: notes.txt lists three action items.\n\n"+noImageMarker {
		t.Fatalf("result=%q err=%v", result, err)
	}
	reqs := h.requests()
	if len(reqs) != 6 || userCount(reqs[4], "could not be attached") == 0 || userCount(reqs[5], "Skip the picture; text only.") != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	h.a.mu.Lock()
	incomplete := h.a.workerOrigins["job-1"].incomplete
	h.a.mu.Unlock()
	if incomplete != nil {
		t.Fatalf("the corrected answer kept the first attempt's reason: %v", incomplete)
	}
}

// Tools off, so no repair: a correction that waits while the image import
// fails is weighed before the failure is committed.
func TestWaitingCorrectionPreemptsAnImageFailure(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			return textReply("Here: ![chart](missing-chart.png)"), 0
		}
		return textReply("Text only: notes.txt lists three action items."), 0
	})
	h.workCfg.VoiceToolsEnabled = false
	var once sync.Once
	events.Subscribe(h.p.Events, func(e events.Info) {
		if strings.Contains(e.Message, "missing-chart.png") {
			once.Do(func() { h.p.TellWork("job-1", "Skip the chart; text only.") })
		}
	})
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	result, err := h.directSteerRun(ctx, "Send a chart of the notes.", nil)
	if err != nil || result != "Text only: notes.txt lists three action items.\n\n"+noImageMarker {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

// The table's budget stop, not the worker waking to a cancelled context,
// decides why an open correction could not apply.
func TestBudgetStopIsALimitForAnOpenCorrection(t *testing.T) {
	inFlight := make(chan struct{})
	var once sync.Once
	stopped := make(chan struct{})
	h := newSteerJobHarnessWith(t, steerSetup{budget: 300 * time.Millisecond}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		once.Do(func() { close(inFlight) })
		<-stopped
		return textReply("Too late."), 0
	})
	var stop sync.Once
	events.Subscribe(h.p.Events, func(events.JobAborted) { stop.Do(func() { close(stopped) }) })
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("receipt=%+v", r)
	}
	select {
	case <-h.aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the budget did not stop the job")
	}
	got := h.events()
	if last := got[len(got)-1]; last.State != "not_applied" || last.Reason != brain.NotAppliedLimit {
		t.Fatalf("correction events=%+v", got)
	}
}

func TestClearedConversationReportsNoCorrections(t *testing.T) {
	h := newSteerJobHarnessWith(t, steerSetup{inbox: true}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("ran"), 0
	})
	release := h.holdSlot()
	defer release()
	id := h.spawn("Read notes.txt in the fixture folder")
	if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlBriefed {
		t.Fatalf("receipt=%+v", r)
	}
	before := len(h.events())
	if err := h.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if got := h.events(); len(got) != before {
		t.Fatalf("a cleared conversation's job reported %+v", got[before:])
	}
	if state := h.steerState(id); state != brain.CorrectionNotApplied {
		t.Fatalf("the correction was left %q", state)
	}
}

func TestOwnJobAndTieredWorkerTakeCorrections(t *testing.T) {
	t.Run("own job", func(t *testing.T) {
		var a *Agency
		var receipt WorkReceipt
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				receipt = a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: steerText})
				return textReply("Read notes.txt."), 0
			}
			return textReply("Read other.txt."), 0
		})
		a = h.a
		h.a.ownFactory = func(_ context.Context, cfg *config.Config) (brain.Provider, error) { return brain.NewOllama(h.workCfg) }
		steer := h.a.newJobSteer(work.WorkScope{})
		h.a.registerSteer("job-1", steer)
		ctx := withJobSteer(job.WithID(t.Context(), "job-1"), steer)
		// The persona talks on Ollama while the configured worker is a CLI.
		own := h.workCfg.Clone()
		own.BrainProvider, own.VoiceWorkerProvider = "ollama", "claude"
		result, err := h.a.ownWorkRun("Read notes.txt.", own, nil)(ctx)
		if err != nil || result != "Read other.txt." || receipt.Jobs[0].Outcome != ControlAccepted {
			t.Fatalf("result=%q err=%v receipt=%+v", result, err, receipt)
		}
	})
	t.Run("tiered local worker", func(t *testing.T) {
		var h *steerJobHarness
		var receipt WorkReceipt
		h = newSteerJobHarnessWith(t, steerSetup{tieredRequests: 10, workerProvider: "claude"}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				receipt = h.p.TellWork(h.lastAccepted(), steerText)
				return textReply("Read notes.txt."), 0
			}
			return textReply("Read other.txt."), 0
		})
		id := h.spawn("Read notes.txt in the fixture folder")
		h.setAccepted(id)
		finished := h.awaitFinished(id)
		if finished.Summary != "Read other.txt." || receipt.Jobs[0].Outcome != ControlAccepted {
			t.Fatalf("delivered %q receipt=%+v", finished.Summary, receipt)
		}
	})
}

// Two calls of a batch finish and the third hits the tool limit; the run
// fails and rolls back, and the saved thread still holds both results.
func TestThreadKeepsCompletedResultsWhenALaterCallFails(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		args := func(path string) api.ToolCallFunctionArguments {
			a := api.NewToolCallFunctionArguments()
			a.Set("path", path)
			return a
		}
		return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{
			{ID: "c1", Function: api.ToolCallFunction{Name: "read_file", Arguments: args("notes.txt")}},
			{ID: "c2", Function: api.ToolCallFunction{Name: "read_file", Arguments: args("other.txt")}},
			{ID: "c3", Function: api.ToolCallFunction{Name: "read_file", Arguments: args("notes.txt")}},
		}}}, 0
	})
	if _, err := h.directSteerRun(t.Context(), "Read the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10, MaxToolCalls: 2})); err == nil {
		t.Fatal("the tool limit did not fail the run")
	}
	h.a.mu.Lock()
	thread := copyWorkerThread(h.a.workerThreads["job-1"])
	h.a.mu.Unlock()
	results := 0
	for _, turn := range thread {
		if turn.Role == "tool" {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("saved thread holds %d results: %+v", results, thread)
	}
}

func TestQueuedCorrectionDispositionsAtTheFirstRequest(t *testing.T) {
	t.Run("worker that cannot report", func(t *testing.T) {
		h := newControlHarness(t, stubbornHookWorker{release: make(chan struct{})}, false)
		var mu sync.Mutex
		var states []string
		events.Subscribe(h.agency.pipeline.Events, func(e events.JobCorrection) {
			mu.Lock()
			states = append(states, e.State)
			mu.Unlock()
		})
		h.agency.jobs.SetWorkerLimits(1, 4)
		hold := make(chan struct{})
		if _, err := h.agency.jobs.Start(context.Background(), job.Spec{Task: "hold", Run: func(context.Context) (string, error) { <-hold; return "", nil }}); err != nil {
			t.Fatal(err)
		}
		h.spawn("list the files here")
		var id string
		for _, info := range h.agency.jobs.Status() {
			if info.Task != "hold" {
				id = info.ID
			}
		}
		if r := h.agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: id, Instruction: steerText}); r.Jobs[0].Outcome != ControlBriefed {
			t.Fatalf("receipt=%+v", r)
		}
		close(hold)
		<-h.tool
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(states, []string{"pending", "briefed"}) {
			t.Fatalf("correction events=%q", states)
		}
		if label := h.agency.steerFor(id).label(); label != "correction added to brief" {
			t.Fatalf("workers label=%q", label)
		}
	})
	t.Run("first request draws no response", func(t *testing.T) {
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			return api.ChatResponse{}, http.StatusBadRequest
		})
		release := h.holdSlot()
		<-h.queued
		id := h.spawn("Read notes.txt in the fixture folder")
		if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlBriefed {
			t.Fatalf("receipt=%+v", r)
		}
		release()
		select {
		case <-h.failed:
		case <-time.After(10 * time.Second):
			t.Fatal("job did not fail")
		}
		got := h.events()
		if len(got) != 2 || got[1].State != "not_applied" || got[1].Reason != brain.NotAppliedFailed {
			t.Fatalf("correction events=%+v", got)
		}
	})
}

// A job asked for a picture is corrected and answers in text: the answer is
// delivered, says no image was attached, and the job and inbox say so too.
func TestCorrectedPictureJobSaysNoImageWasAttached(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "inbox"}[inbox], func(t *testing.T) {
			var h *steerJobHarness
			h = newSteerJobHarnessWith(t, steerSetup{inbox: inbox}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
				if n == 1 {
					h.p.TellWork(h.lastAccepted(), "No picture; describe it in text.")
					return readCall("notes.txt"), 0
				}
				return textReply("A plain chart of the notes, described."), 0
			})
			id := h.spawn("Send me a picture of the notes")
			h.setAccepted(id)
			want := "A plain chart of the notes, described.\n\n" + noImageMarker
			if inbox {
				outcome := h.awaitOutcome(id)
				if outcome.Status != work.OutcomeSucceeded || outcome.Limitations != "no image attached" || outcome.Text != want {
					t.Fatalf("outcome=%+v", outcome)
				}
				return
			}
			finished := h.awaitFinished(id)
			if !finished.NoImage || finished.CutShort || finished.Summary != want {
				t.Fatalf("delivered %+v", finished)
			}
		})
	}
}

// Once the table has ended a job, a correction to it is refused, even from a
// callback that runs before the job's own done callback.
func TestTellAfterTheTableEndedTheJobIsRefused(t *testing.T) {
	t.Run("cancel all, then a tell from the first abort", func(t *testing.T) {
		inFlight, release := make(chan struct{}), make(chan struct{})
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			close(inFlight)
			<-release
			return textReply("Too late."), 0
		})
		defer close(release)
		h.table.SetWorkerLimits(1, 4)
		running := h.spawn("Read notes.txt in the fixture folder")
		<-inFlight
		queued := h.spawn("Read other.txt in the fixture folder")
		var once sync.Once
		var receipt WorkReceipt
		events.Subscribe(h.p.Events, func(e events.JobAborted) {
			if e.ID == running {
				once.Do(func() { receipt = h.p.TellWork(queued, steerText) })
			}
		})
		if err := h.p.StopWorkers(); err != nil {
			t.Fatal(err)
		}
		if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlFinished {
			t.Fatalf("a tell to a job the table had ended: %+v", receipt)
		}
	})
	t.Run("cancel during the start announcement, then a tell", func(t *testing.T) {
		h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			t.Error("a job cancelled while it was announced ran")
			return textReply("ran"), 0
		})
		var once sync.Once
		var receipt WorkReceipt
		events.Subscribe(h.p.Events, func(e events.JobStarted) {
			once.Do(func() {
				if err := h.p.CancelWork(e.ID); err != nil {
					t.Error(err)
				}
				receipt = h.p.TellWork(e.ID, steerText)
			})
		})
		h.spawn("Read notes.txt in the fixture folder")
		select {
		case <-h.aborted:
		case <-time.After(5 * time.Second):
			t.Fatal("the job was not stopped")
		}
		if len(receipt.Jobs) != 1 || receipt.Jobs[0].Outcome != ControlFinished {
			t.Fatalf("a tell after the stop had won: %+v", receipt)
		}
	})
}

// The answer in hand is delivered when the run that would replace it fails:
// at finalization, or at a later request after completing tools, without
// losing the tools the corrected run completed from the saved thread.
func TestHeldAnswerStandsWhenItsReplacementFails(t *testing.T) {
	t.Run("replacement's image import fails", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				h.p.TellWork(h.lastAccepted(), "Attach the chart too.")
				return textReply("notes.txt lists three action items."), 0
			}
			return textReply("![chart](missing-chart.png)"), 0
		})
		id := h.spawn("Read notes.txt in the fixture folder")
		h.setAccepted(id)
		finished := h.awaitFinished(id)
		if finished.Summary != "notes.txt lists three action items." || !finished.CutShort || finished.CutShortReason != string(brain.ForcedByFailedCorrection) {
			t.Fatalf("delivered %+v", finished)
		}
	})
	t.Run("replacement fails after a tool", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			switch n {
			case 1:
				h.p.TellWork(h.lastAccepted(), "Read other.txt too.")
				return textReply("notes.txt lists three action items."), 0
			case 2:
				return readCall("other.txt"), 0
			}
			return api.ChatResponse{}, http.StatusBadRequest
		})
		id := h.spawn("Read notes.txt in the fixture folder")
		h.setAccepted(id)
		finished := h.awaitFinished(id)
		if finished.Summary != "notes.txt lists three action items." || !finished.CutShort {
			t.Fatalf("delivered %+v", finished)
		}
		info := onlyWorker(t, h.p)
		if !slices.ContainsFunc(info.Thread, func(turn brain.Turn) bool { return turn.Role == "tool" && strings.Contains(turn.Content, "other.txt") }) {
			t.Fatalf("the corrected run's completed tool was lost from the thread: %+v", info.Thread)
		}
	})
}

// A correction keeps the task's picture: an answer without one gets the one
// repair, asked with the brief and the corrections in order.
func TestCorrectedPictureJobIsRepairedWithItsCorrections(t *testing.T) {
	var a *Agency
	var root string
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Use Tuesday's figures for the chart."})
			return readCall("notes.txt"), 0
		case 2:
			return textReply("The chart uses Tuesday's figures."), 0
		}
		return textReply("Here it is.\n![Chart](<" + filepath.Join(root, "chart.png") + ">)"), 0
	})
	a, root = h.a, h.root
	if err := os.WriteFile(filepath.Join(root, "chart.png"), testmedia.PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	result, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10}))
	if err != nil || strings.Contains(result, noImageMarker) {
		t.Fatalf("result=%q err=%v", result, err)
	}
	reqs := h.requests()
	if len(reqs) != 3 || userCount(reqs[2], "could not be attached") == 0 || userCount(reqs[2], "Correction from the user: Use Tuesday's figures for the chart.") == 0 {
		t.Fatalf("requests=%d; the repair did not carry the correction", len(reqs))
	}
	if refs := h.a.workerReplyMedia("job-1"); len(refs) != 1 {
		t.Fatalf("delivered images=%d", len(refs))
	}
}

// A correction reaches a job only from the conversation that started it; a
// cleared conversation's job is unknown to a tell, though still cancellable.
func TestTellDoesNotCrossAClearedConversation(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		close(inFlight)
		<-release
		return textReply("Done."), 0
	})
	defer close(release)
	if err := h.p.BeginConversationIdentity("conversation-1"); err != nil {
		t.Fatal(err)
	}
	admitted := h.p.ConversationScope()
	id := h.spawn("Read notes.txt in the fixture folder")
	<-inFlight
	if r := h.p.TellWork(id, "Before the clear."); r.Jobs[0].Outcome != ControlAccepted {
		t.Fatalf("receipt=%+v", r)
	}
	if got := h.events(); len(got) != 1 || got[0].Scope != admitted || !admitted.Valid() {
		t.Fatalf("correction event scope=%+v, admitted %+v", got, admitted)
	}
	if err := h.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if r := h.p.TellWork(id, "After the clear."); len(r.Jobs) != 1 || r.Jobs[0].Outcome != ControlUnknown {
		t.Fatalf("a tell crossed the clear: %+v", r)
	}
	if err := h.p.CancelWork(id); err != nil {
		t.Fatalf("the leftover job can no longer be cancelled: %v", err)
	}
}

// Taking corrections costs a job no model metadata request of its own: the
// check reuses what the job's validation fetched.
func TestTakingCorrectionsAddsNoMetadataRequest(t *testing.T) {
	h := newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("Done."), 0
	})
	count := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.shows
	}
	before := count()
	if _, err := h.a.workerRunWithBudget("Read notes.txt.", h.worker, h.workCfg, nil, true)(job.WithID(t.Context(), "job-0")); err != nil {
		t.Fatal(err)
	}
	plain := count() - before
	before = count()
	if _, err := h.directSteerRun(t.Context(), "Read notes.txt.", nil); err != nil {
		t.Fatal(err)
	}
	if steered := count() - before; steered != plain {
		t.Fatalf("metadata requests: %d with corrections, %d without", steered, plain)
	}
}

// textWorker answers every request with one reply and keeps no input.
type textWorker struct{ reply string }

func (w textWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{w.reply}}).ThinkStream(ctx, input, opts)
}
func (w textWorker) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return w.reply, nil
}
func (textWorker) ClearHistory()            {}
func (textWorker) History() []brain.Turn    { return nil }
func (textWorker) LoadHistory([]brain.Turn) {}

// The no-image marker is applied once, after the hand-off cleaner, and the
// delivered text and the saved thread agree, whichever kind of worker ran.
func TestNoImageMarkerIsAppliedOnceToTheFinishedAnswer(t *testing.T) {
	const handoff = "\n\nNow give the result to the user as Ada, briefly (2–3 sentences). No markdown."
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	t.Run("worker that takes no input", func(t *testing.T) {
		h := newSteerJobHarness(t, func(int, api.ChatRequest) (api.ChatResponse, int) { return textReply("unused"), 0 })
		steer := h.a.newJobSteer(work.WorkScope{})
		h.a.registerSteer("job-1", steer)
		steer.control.Steer("Text only, please.")
		run := withJobSteer(job.WithID(ctx, "job-1"), steer)
		result, err := h.a.workerRunWithBudget("Send a picture of the notes.", textWorker{reply: "Text only, as asked." + handoff}, h.workCfg, nil, false)(run)
		if err != nil || result != "Text only, as asked.\n\n"+noImageMarker {
			t.Fatalf("result=%q err=%v", result, err)
		}
	})
	t.Run("worker that takes corrections", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			if n == 1 {
				h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Text only, please."})
				return readCall("notes.txt"), 0
			}
			return textReply("Text only, as asked." + handoff), 0
		})
		result, err := h.directSteerRun(ctx, "Send a picture of the notes.", nil)
		if err != nil || result != "Text only, as asked.\n\n"+noImageMarker {
			t.Fatalf("result=%q err=%v", result, err)
		}
		h.a.mu.Lock()
		thread := copyWorkerThread(h.a.workerThreads["job-1"])
		h.a.mu.Unlock()
		if last := thread[len(thread)-1]; last.Role != "assistant" || last.Content != result {
			t.Fatalf("saved thread ends %q, delivered %q", last.Content, result)
		}
	})
}

// The budget expires while a failed image import is being reported: the
// table's stop, not the import failure, decides why the correction could not
// apply.
func TestBudgetStopDuringAFailedImportIsALimit(t *testing.T) {
	var h *steerJobHarness
	h = newSteerJobHarnessWith(t, steerSetup{budget: 300 * time.Millisecond}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		h.p.TellWork(h.lastAccepted(), steerText)
		return textReply("![chart](missing-chart.png)"), 0
	})
	stopped := make(chan struct{})
	var stop, block sync.Once
	events.Subscribe(h.p.Events, func(events.JobAborted) { stop.Do(func() { close(stopped) }) })
	events.Subscribe(h.p.Events, func(e events.Info) {
		if strings.Contains(e.Message, "missing-chart.png") {
			block.Do(func() { <-stopped })
		}
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	h.setAccepted(id)
	select {
	case <-h.aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the budget did not stop the job")
	}
	if state := h.steerState(id); state != brain.CorrectionNotApplied {
		t.Fatalf("state=%q", state)
	}
	if c := h.a.steerFor(id).control.Corrections(); c[len(c)-1].Reason != brain.NotAppliedLimit {
		t.Fatalf("corrections=%+v", c)
	}
}

// A corrected picture job whose repair brings no usable replacement delivers
// the answer it had, marked; and it never gets a second repair.
func TestCorrectedPictureJobKeepsItsAnswerAndRepairsOnce(t *testing.T) {
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	tell := func(a *Agency) {
		a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Use Tuesday's figures."})
	}
	repairs := func(reqs []api.ChatRequest) int {
		n := 0
		for _, r := range reqs {
			if userCount(r, "could not be attached") > 0 && strings.Contains(r.Messages[len(r.Messages)-1].Content, "could not be attached") {
				n++
			}
		}
		return n
	}
	for name, repair := range map[string]func() (api.ChatResponse, int){
		"repair returns only a broken image": func() (api.ChatResponse, int) { return textReply("![chart](missing-chart.png)"), 0 },
		"repair request fails":               func() (api.ChatResponse, int) { return api.ChatResponse{}, http.StatusBadRequest },
	} {
		t.Run(name, func(t *testing.T) {
			var h *steerJobHarness
			h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
				switch n {
				case 1:
					tell(h.a)
					return readCall("notes.txt"), 0
				case 2:
					return textReply("Tuesday's figures: three items."), 0
				}
				return repair()
			})
			result, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10}))
			if err != nil || result != "Tuesday's figures: three items.\n\n"+noImageMarker {
				t.Fatalf("result=%q err=%v", result, err)
			}
		})
	}
	t.Run("a tell during the repair", func(t *testing.T) {
		var h *steerJobHarness
		h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
			switch n {
			case 1:
				return textReply("Three items, no chart."), 0
			case 2:
				tell(h.a)
				return textReply("Still no chart."), 0
			}
			return textReply("Tuesday's figures: three items."), 0
		})
		result, err := h.directSteerRun(ctx, "Send a chart of the notes.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 10}))
		if err != nil || result != "Tuesday's figures: three items.\n\n"+noImageMarker {
			t.Fatalf("result=%q err=%v", result, err)
		}
		if n := repairs(h.requests()); n != 1 {
			t.Fatalf("repairs=%d", n)
		}
	})
}

// Answer A is held; its replacement B comes back with a broken picture while a
// second correction waits, and no request is left: A is delivered, not B's
// failure.
func TestLimitDeliversTheLastFinishedAnswer(t *testing.T) {
	var h *steerJobHarness
	h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		switch n {
		case 1:
			h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Add a chart."})
			return textReply("notes.txt lists three items."), 0
		}
		h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: "job-1", Instruction: "Use Tuesday's figures."})
		return textReply("![chart](missing-chart.png)"), 0
	})
	result, err := h.directSteerRun(t.Context(), "Read notes.txt.", brain.NewWorkerBudget(brain.BudgetLimits{MaxRequests: 3}))
	if err != nil || result != "notes.txt lists three items." {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestInboxCancelOfAQueuedJobReportsItsCorrection(t *testing.T) {
	h := newSteerJobHarnessWith(t, steerSetup{inbox: true}, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		return textReply("ran"), 0
	})
	release := h.holdSlot()
	defer release()
	id := h.spawn("Read notes.txt in the fixture folder")
	if r := h.p.TellWork(id, steerText); r.Jobs[0].Outcome != ControlBriefed {
		t.Fatalf("receipt=%+v", r)
	}
	if err := h.p.CancelWork(id); err != nil {
		t.Fatal(err)
	}
	if got := h.states(); !slices.Equal(got, []string{"pending", "superseded"}) {
		t.Fatalf("correction events=%q", got)
	}
}

func TestHeldAnswerIsNotShownAsReplacedWhenItIsDelivered(t *testing.T) {
	var h *steerJobHarness
	h = newSteerJobHarness(t, func(n int, req api.ChatRequest) (api.ChatResponse, int) {
		if n == 1 {
			h.p.TellWork(h.lastAccepted(), "Attach the chart too.")
			return textReply("notes.txt lists three action items."), 0
		}
		return textReply("![chart](missing-chart.png)"), 0
	})
	id := h.spawn("Read notes.txt in the fixture folder")
	h.setAccepted(id)
	finished := h.awaitFinished(id)
	info := onlyWorker(t, h.p)
	for _, turn := range info.Thread {
		if turn.Role == "assistant" && turn.Content == finished.Summary && turn.Disposition == brain.DispositionSuperseded {
			t.Fatalf("the delivered answer is shown as replaced: %+v", turn)
		}
	}
}
