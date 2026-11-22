package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// stubbornHookWorker reports one tool call and then returns only when
// released, whatever its context says: a worker slow to stop.
type stubbornHookWorker struct {
	release <-chan struct{}
}

func (w stubbornHookWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{"done"}}).ThinkStream(ctx, input, opts)
}

func (w stubbornHookWorker) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	call := brain.ToolCall{ID: "call_1", Name: "list_files", Kind: brain.KindListDir, Summary: "."}
	if opts.OnToolStart != nil {
		opts.OnToolStart(call)
	}
	if opts.OnToolEnd != nil {
		opts.OnToolEnd(brain.ToolResult{ToolCall: call, Preview: "README.md"})
	}
	<-w.release
	return "", context.Canceled
}

func (stubbornHookWorker) ClearHistory()            {}
func (stubbornHookWorker) History() []brain.Turn    { return nil }
func (stubbornHookWorker) LoadHistory([]brain.Turn) {}

// controlHarness is a budgetHarness whose worker can be swapped and whose
// bus is also tapped for exits and the reply shown on screen.
type controlHarness struct {
	*budgetHarness
	exited chan events.JobExited
	shown  chan string
}

func newControlHarness(t *testing.T, worker brain.Provider, inbox bool) *controlHarness {
	t.Helper()
	h := &controlHarness{budgetHarness: newBudgetHarness(t), exited: make(chan events.JobExited, 4), shown: make(chan string, 4)}
	p := h.agency.pipeline
	events.Subscribe(p.Events, func(e events.JobExited) { h.exited <- e })
	events.Subscribe(p.Events, func(e events.ResponseReady) { h.shown <- e.Response })
	cfg := &config.Config{}
	if inbox {
		cfg = &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker"}
	}
	h.agency.ReplaceWorker(worker, cfg, nil)
	if inbox {
		if err := p.EnableConversationWork("control", nil, cfg); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.CloseWork() })
	}
	return h
}

// lastSpoken is the newest line, past the acknowledgements of the spawns.
func (h *controlHarness) lastSpoken() string {
	calls := h.tts.Calls()
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1]
}

func (h *controlHarness) awaitExited(t *testing.T) events.JobExited {
	t.Helper()
	select {
	case e := <-h.exited:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("the worker's exit never surfaced")
		return events.JobExited{}
	}
}

func onlyWorker(t *testing.T, p *Pipeline) WorkerInfo {
	t.Helper()
	items := p.Workers()
	if len(items) != 1 {
		t.Fatalf("workers = %+v, want one", items)
	}
	return items[0]
}

func TestUserCancelSaysWorkerStillStoppingWhenItHasNotReturned(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		name := "legacy"
		if inbox {
			name = "inbox"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			h := newControlHarness(t, stubbornHookWorker{release: release}, inbox)
			h.agency.cancelExitWait = 20 * time.Millisecond
			h.spawn("list the files here")
			h.awaitTool(t)

			h.cancel("kill it")
			want := "Cancelled; the worker is still stopping."
			if inbox {
				want = "Cancellation requested; the worker is still stopping. Completed results remain in the work inbox."
			}
			if got := h.lastSpoken(); got != want {
				t.Fatalf("spoken = %q, want %q", got, want)
			}
			if !inbox {
				if e := h.awaitAborted(t); !e.Stopping {
					t.Fatalf("aborted = %+v, want it marked stopping", e)
				}
			}
			p := h.agency.pipeline
			if item := onlyWorker(t, p); item.State != "stopping" || item.Termination != "unknown" {
				t.Fatalf("worker after cancel = %+v, want stopping with termination unknown", item)
			}
			if h.agency.jobs.Active() {
				t.Fatal("a stopping job still holds its slot")
			}

			close(release)
			if e := h.awaitExited(t); e.ID != "job-1" && !strings.HasPrefix(e.ID, "work-") {
				t.Fatalf("exit for %q", e.ID)
			}
			item := onlyWorker(t, p)
			if item.State == "stopping" || item.Termination != "confirmed (worker returned)" {
				t.Fatalf("worker after its exit = %+v, want ended with the return confirmed", item)
			}
			if got := h.lastSpoken(); got != want {
				t.Fatalf("spoken after the exit = %q, want the exit unspoken", got)
			}
		})
	}
}

func TestUserCancelInInboxSaysRequestedAndConfirmsTheExit(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newControlHarness(t, gatedHookWorker{release: release}, true)
	h.spawn("list the files here")
	h.awaitTool(t)
	h.cancel("cancel")
	if got, want := h.lastSpoken(), "Cancellation requested. Completed results remain in the work inbox."; got != want {
		t.Fatalf("spoken = %q, want %q", got, want)
	}
	if item := onlyWorker(t, h.agency.pipeline); item.State != "cancelled" || item.Termination != "confirmed (worker returned)" {
		t.Fatalf("worker = %+v, want cancelled with the return confirmed", item)
	}
}

// A keyboard cancel returns at once even when the worker does not stop;
// its exit arrives later as an event.
func TestCommandCancelDoesNotWaitForTheWorker(t *testing.T) {
	release := make(chan struct{})
	h := newControlHarness(t, stubbornHookWorker{release: release}, false)
	h.agency.cancelExitWait = time.Hour
	h.spawn("list the files here")
	h.awaitTool(t)

	returned := make(chan error, 1)
	go func() { returned <- h.agency.pipeline.CancelWork("job-1") }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelWork waited for the worker")
	}
	if item := onlyWorker(t, h.agency.pipeline); item.State != "stopping" {
		t.Fatalf("worker = %+v, want stopping", item)
	}
	close(release)
	h.awaitExited(t)
}

func TestControlWorkReceipts(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newControlHarness(t, gatedHookWorker{release: release}, false)
	h.spawn("list the files here")
	h.awaitTool(t)
	h.spawn("tidy the notes")

	cmd := WorkActor{Kind: WorkActorCommand}
	if r := h.agency.ControlWork(cmd, WorkRequest{Action: WorkCancel, JobID: "job-9"}); !errors.Is(r.Err(), work.ErrUnknown) || r.Jobs[0].Outcome != ControlUnknown {
		t.Fatalf("unknown id receipt = %+v", r)
	}
	if r := h.agency.ControlWork(cmd, WorkRequest{Action: "report", JobID: "job-1"}); r.Rejected == "" || r.Err() == nil {
		t.Fatalf("unsupported action receipt = %+v", r)
	}
	if r := h.agency.ControlWork(cmd, WorkRequest{Action: WorkCancel}); r.Rejected == "" {
		t.Fatalf("no id receipt = %+v", r)
	}
	r := h.agency.ControlWork(cmd, WorkRequest{Action: WorkCancel, JobID: "job-2"})
	if got := r.Jobs[0]; got.Outcome != ControlStopped || !got.Queued {
		t.Fatalf("queued cancel = %+v, want stopped while queued", got)
	}
	r = h.agency.ControlWork(cmd, WorkRequest{Action: WorkCancel, JobID: "job-1"})
	if got := r.Jobs[0]; (got.Outcome != ControlStopped && got.Outcome != ControlStopping) || got.Queued {
		t.Fatalf("running cancel = %+v", got)
	}
	r = h.agency.ControlWork(cmd, WorkRequest{Action: WorkCancel, JobID: "job-1"})
	if got := r.Jobs[0]; got.Outcome != ControlFinished || r.Err() != nil {
		t.Fatalf("second cancel = %+v, want already finished and no error", got)
	}
}

// The cancel line keeps its spoken text; the history also records what the
// cancelled job had done, marked as unfinished, without speaking it.
func TestCancelRecordsWhatTheJobHadDoneInHistoryOnly(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newControlHarness(t, gatedHookWorker{release: release}, false)
	transcript := &recordedTranscript{}
	p := h.agency.pipeline
	p.Brain, p.Transcript = &fakeBrain{chunks: []string{"unused"}}, transcript
	h.spawn("list the files here")
	h.awaitTool(t)

	metrics := newTurnMetrics()
	if err := p.completeRoutedTurn(context.Background(), p.newTurnConductor(metrics), metrics, "kill it", RouteJobAbort, "phrase"); err != nil {
		t.Fatal(err)
	}
	spoken := h.lastSpoken()
	if !strings.HasPrefix(spoken, "Stopped") || strings.Contains(spoken, "Done so far") {
		t.Fatalf("spoken = %q, want only the stop line", spoken)
	}
	if shown := <-h.shown; shown != spoken {
		t.Fatalf("shown reply = %q, want the spoken line %q", shown, spoken)
	}
	turns := transcript.Turns()
	last := turns[len(turns)-1]
	want := spoken + " [job-1 (list the files here) was cancelled before it finished. Done so far: listed 1 folder"
	if last.Role != "assistant" || !strings.HasPrefix(last.Content, want) || !strings.HasSuffix(last.Content, ".]") {
		t.Fatalf("history reply = %q, want it to start %q", last.Content, want)
	}
}

// Cancel-all in legacy mode ends running and queued jobs as one act: no
// queued job starts on a slot a cancelled one freed.
func TestPhraseCancelStartsNoQueuedJob(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newControlHarness(t, gatedHookWorker{release: release}, false)
	started := make(chan string, 8)
	events.Subscribe(h.agency.pipeline.Events, func(e events.JobStarted) { started <- e.ID })
	h.spawn("audit the config")
	h.awaitTool(t)
	<-started
	h.spawn("write the report")
	h.spawn("tidy the notes")

	h.cancel("cancel")
	if got := h.lastSpoken(); got != "Stopped. Dropped 2 queued jobs." && !strings.HasPrefix(got, "Stopped after") {
		t.Fatalf("spoken = %q", got)
	}
	for range 3 {
		h.awaitAborted(t)
	}
	select {
	case id := <-started:
		t.Fatalf("%s started during the cancel", id)
	default:
	}
}

// agent_cancel reaches only the calling runtime's own task: never another
// runtime's ticket and never a table job.
func TestAgentCancelReachesOnlyItsOwnRuntimesTask(t *testing.T) {
	makeRuntime := func() (*delegation.Runtime, *managedHarnessClient) {
		client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
		runtime, err := delegation.New(t.Context(), client, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runtime.Close() })
		return runtime, client
	}
	first, firstClient := makeRuntime()
	next, _ := makeRuntime()
	oldBrain := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: first, closed: make(chan struct{})}
	nextBrain := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: next, closed: make(chan struct{})}
	cfg := &config.Config{VoiceWorkerProvider: "ollama"}
	p := &Pipeline{Events: events.NewBus(), Brain: oldBrain}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.AbortAll)
	a := NewAgency(p, jobs, &lifecycleWorker{}, cfg, nil)
	a.Wire()
	if err := p.EnableConversationWork("agent-cancel", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	ticket, err := first.Start("managed task")
	if err != nil {
		t.Fatal(err)
	}
	<-firstClient.started
	if err := p.ReplaceConversationBrain(nextBrain, &lifecycleWorker{}, cfg, nil); err != nil {
		t.Fatal(err)
	}
	tableStarted := make(chan struct{})
	tableJob, err := jobs.Start(t.Context(), job.Spec{Task: "table job", Run: func(ctx context.Context) (string, error) {
		close(tableStarted)
		<-ctx.Done()
		return "", ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-tableStarted

	for _, id := range []string{ticket, tableJob} {
		err := next.RequestCancel(id)
		if err == nil || !strings.Contains(err.Error(), "unknown background task") {
			t.Fatalf("agent_cancel(%s) from another runtime = %v, want refused as unknown", id, err)
		}
	}
	select {
	case <-firstClient.canceled:
		t.Fatal("another runtime's task was cancelled")
	default:
	}
	if st := jobs.Status(); st[0].State != job.StateRunning {
		t.Fatalf("table job = %+v, want still running", st[0])
	}
	if err := first.RequestCancel(ticket); err != nil {
		t.Fatalf("agent_cancel of its own task = %v", err)
	}
	select {
	case <-firstClient.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("its own task was not cancelled")
	}
}

// managedBrain exposes its delegation runtime the way an Ollama brain does.
type managedBrain struct {
	*managedHarnessBrain
}

func (b managedBrain) ManagedRuntime() *delegation.Runtime { return b.runtime }

// In legacy mode an active managed delegation arms the cancel phrases, and a
// spoken cancel reaches it; the status phrases stay unarmed.
func TestCancelPhraseReachesManagedDelegation(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime, err := delegation.New(t.Context(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	tts := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	p := &Pipeline{TTS: tts, Player: player, Events: events.NewBus(),
		Brain: managedBrain{&managedHarnessBrain{Provider: &lifecycleWorker{reply: "live"}, runtime: runtime, closed: make(chan struct{})}}}
	a := NewAgency(p, job.NewTable(t.Context()), &lifecycleWorker{}, &config.Config{}, nil)
	a.Wire()

	if _, ok := classifyPhrase("cancel", p.routeContext()); ok {
		t.Fatal("the cancel phrase was armed with nothing running")
	}
	if _, err := runtime.Start("managed task"); err != nil {
		t.Fatal(err)
	}
	<-client.started
	ctx := p.routeContext()
	if kind, ok := classifyPhrase("cancel", ctx); !ok || kind != RouteJobAbort {
		t.Fatalf("cancel with a managed task = %q, %v; want job.abort", kind, ok)
	}
	if _, ok := classifyPhrase("status", ctx); ok {
		t.Fatal("a managed task armed the status phrases, whose answer reads only the job table")
	}

	metrics := newTurnMetrics()
	a.HandleRoute(context.Background(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteJobAbort, Transcript: "cancel", Source: "phrase"})
	select {
	case <-client.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("the cancel phrase did not reach the managed task")
	}
	if got := strings.Join(tts.Calls(), " | "); got != "Asked Obey to cancel the delegated task." {
		t.Fatalf("spoken = %q", got)
	}
}

func TestCancelLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ran              []time.Duration
		stopping, queued int
		want             string
	}{
		{[]time.Duration{40 * time.Second}, 0, 0, "Stopped after 40 seconds."},
		{[]time.Duration{40 * time.Second}, 1, 0, "Cancelled after 40 seconds; the worker is still stopping."},
		{[]time.Duration{200 * time.Millisecond}, 1, 1, "Cancelled; the worker is still stopping. Dropped 1 queued job."},
		{[]time.Duration{3 * time.Second, 90 * time.Second}, 2, 0, "Cancelled 2 jobs. The longest ran 1 minute 30 seconds; the workers are still stopping."},
		{[]time.Duration{3 * time.Second, 90 * time.Second, 5 * time.Second}, 1, 0, "Cancelled 3 jobs. The longest ran 1 minute 30 seconds; one worker is still stopping."},
		{[]time.Duration{0, 400 * time.Millisecond, 0}, 2, 2, "Cancelled 3 jobs; 2 workers are still stopping. Dropped 2 queued jobs."},
	}
	for _, tc := range cases {
		if got := cancelLine(tc.ran, tc.stopping, tc.queued); got != tc.want {
			t.Errorf("cancelLine(%v, %d, %d) = %q, want %q", tc.ran, tc.stopping, tc.queued, got, tc.want)
		}
	}
}
