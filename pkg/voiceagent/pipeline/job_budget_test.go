package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// budgetHarness is an Agency over a gated worker, with the bus tapped for
// the job's abort and the worker's first tool call.
type budgetHarness struct {
	agency  *Agency
	tts     *progressiveFakeTTS
	aborted chan events.JobAborted
	tool    chan struct{}
	release chan struct{}
	turn    *turnConductor
	metrics *turnMetrics
}

func newBudgetHarness(t *testing.T, opts ...job.Option) *budgetHarness {
	t.Helper()
	h := &budgetHarness{
		tts:     &progressiveFakeTTS{},
		aborted: make(chan events.JobAborted, 4),
		tool:    make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	bus := events.NewBus()
	events.Subscribe(bus, func(e events.JobAborted) { h.aborted <- e })
	events.Subscribe(bus, func(e events.ToolCallStarted) {
		if e.JobID != "" {
			h.tool <- struct{}{}
		}
	})
	p := &Pipeline{TTS: h.tts, Player: player, Events: bus}
	h.agency = NewAgency(p, job.NewTable(context.Background(), opts...), gatedHookWorker{release: h.release}, &config.Config{}, nil)
	h.agency.Wire()
	h.metrics = newTurnMetrics()
	h.turn = p.newTurnConductor(h.metrics)
	return h
}

func (h *budgetHarness) spawn(task string) {
	h.agency.HandleRoute(context.Background(), h.turn, h.metrics, RouteDecision{Kind: RouteSpawn, Transcript: task, Source: "test"})
}

func (h *budgetHarness) spoken() string { return strings.Join(h.tts.Calls(), " | ") }

func TestBudgetStopSpeaksOnceAndNamesTheReason(t *testing.T) {
	h := newBudgetHarness(t, job.WithBudget(60*time.Millisecond))
	defer close(h.release)
	h.spawn("list the files here")

	var e events.JobAborted
	select {
	case e = <-h.aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("the budget never stopped the job")
	}
	if e.Reason != events.AbortBudget {
		t.Fatalf("reason = %q, want %q", e.Reason, events.AbortBudget)
	}
	waitUntil(t, 2*time.Second, "the budget line to be spoken", func() bool {
		return strings.Contains(h.spoken(), "ran past its budget")
	})
	if got := h.spoken(); strings.Count(got, "ran past its budget") != 1 {
		t.Fatalf("spoken = %q, want the budget line exactly once", got)
	}
}

func (h *budgetHarness) cancel(phrase string) {
	h.agency.HandleRoute(context.Background(), h.turn, h.metrics, RouteDecision{Kind: RouteJobAbort, Transcript: phrase, Source: "phrase"})
}

func (h *budgetHarness) awaitAborted(t *testing.T) events.JobAborted {
	t.Helper()
	select {
	case e := <-h.aborted:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("abort never surfaced")
		return events.JobAborted{}
	}
}

func TestUserCancelSpeaksStoppedOnceAndSaysCancelled(t *testing.T) {
	h := newBudgetHarness(t)
	defer close(h.release)
	h.spawn("list the files here")
	select {
	case <-h.tool:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never ran a tool")
	}

	h.cancel("kill it")
	if e := h.awaitAborted(t); e.Reason != events.AbortCancelled {
		t.Fatalf("reason = %q, want %q", e.Reason, events.AbortCancelled)
	}
	time.Sleep(50 * time.Millisecond)
	got := h.spoken()
	if strings.Count(got, "Stopped") != 1 {
		t.Fatalf("spoken = %q, want one stop line", got)
	}
	if strings.Contains(got, "budget") || strings.Contains(got, "Dropped") {
		t.Fatalf("spoken = %q, want no budget or queue talk for one running job", got)
	}
}

func TestUserCancelDropsQueuedJobsInTheSameLine(t *testing.T) {
	h := newBudgetHarness(t)
	defer close(h.release)
	h.spawn("audit the config")
	select {
	case <-h.tool:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never ran a tool")
	}
	h.spawn("write the report")

	h.cancel("cancel")
	ids := map[string]string{}
	for i := 0; i < 2; i++ {
		e := h.awaitAborted(t)
		ids[e.ID] = e.Reason
	}
	if len(ids) != 2 {
		t.Fatalf("aborted ids = %v, want both jobs", ids)
	}
	for id, reason := range ids {
		if reason != events.AbortCancelled {
			t.Fatalf("%s reason = %q, want %q", id, reason, events.AbortCancelled)
		}
	}
	time.Sleep(50 * time.Millisecond)
	got := h.spoken()
	if strings.Count(got, "Stopped") != 1 || strings.Count(got, "Dropped 1 queued job.") != 1 {
		t.Fatalf("spoken = %q, want one stop line naming the dropped job", got)
	}
	if h.agency.jobs.Active() {
		t.Fatal("jobs still active after cancel, want none")
	}
}

func TestAbortLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ran    []time.Duration
		queued int
		want   string
	}{
		{nil, 0, "No job is running."},
		{[]time.Duration{12 * time.Second}, 0, "Stopped after 12 seconds."},
		{[]time.Duration{12 * time.Second}, 2, "Stopped after 12 seconds. Dropped 2 queued jobs."},
		{[]time.Duration{3 * time.Second, 90 * time.Second}, 1, "Stopped 2 jobs. The longest ran 1 minute 30 seconds. Dropped 1 queued job."},
		{nil, 1, "Dropped 1 queued job."},
		{[]time.Duration{300 * time.Millisecond}, 0, "Stopped."},
		{[]time.Duration{0, 400 * time.Millisecond}, 1, "Stopped 2 jobs. Dropped 1 queued job."},
	}
	for _, tc := range cases {
		if got := abortLine(tc.ran, tc.queued); got != tc.want {
			t.Errorf("abortLine(%v, %d) = %q, want %q", tc.ran, tc.queued, got, tc.want)
		}
	}
}

func TestSpokenTaskLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"audit the config", "audit the config"},
		{"list the files here.", "list the files here"},
		{"create a file called README.md with one line about this folder, then list the files here and read the README back",
			"create a file called README.md with one line about this…"},
		{"search the web for rust; summarize what you find", "search the web for rust"},
		{"read the README and then tell me what it says", "read the README"},
		{"", "that"},
	}
	for _, tc := range cases {
		if got := spokenTaskLabel(tc.in); got != tc.want {
			t.Errorf("spokenTaskLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJobStatusCountsRunningAndQueued(t *testing.T) {
	h := newBudgetHarness(t, job.WithMaxConcurrent(2))
	defer close(h.release)
	for _, task := range []string{"audit the config", "write the report", "tidy the notes"} {
		h.spawn(task)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-h.tool:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d job(s) reached a tool, want 2 running", i)
		}
	}

	h.agency.HandleRoute(context.Background(), h.turn, h.metrics, RouteDecision{Kind: RouteJobStatus, Transcript: "what's happening", Source: "test"})
	got := h.spoken()
	if !strings.Contains(got, "Still working on audit the config.") {
		t.Fatalf("spoken = %q, want the first running job named", got)
	}
	if !strings.Contains(got, "Also 1 more running and 1 queued.") {
		t.Fatalf("spoken = %q, want the other running job and the queued one counted", got)
	}
}

func TestSpokenDuration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45 seconds"},
		{time.Second, "1 second"},
		{time.Minute, "1 minute"},
		{10 * time.Minute, "10 minutes"},
		{6*time.Minute + 5*time.Second, "6 minutes 5 seconds"},
	} {
		if got := spokenDuration(tc.d); got != tc.want {
			t.Errorf("spokenDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
