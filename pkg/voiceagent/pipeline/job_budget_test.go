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

func TestUserCancelStaysSilentAndSaysCancelled(t *testing.T) {
	h := newBudgetHarness(t)
	defer close(h.release)
	h.spawn("list the files here")
	select {
	case <-h.tool:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never ran a tool")
	}

	h.agency.handleAbort()
	var e events.JobAborted
	select {
	case e = <-h.aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("abort never surfaced")
	}
	if e.Reason != events.AbortCancelled {
		t.Fatalf("reason = %q, want %q", e.Reason, events.AbortCancelled)
	}
	time.Sleep(50 * time.Millisecond)
	if got := h.spoken(); strings.Contains(got, "stopped") || strings.Contains(got, "budget") {
		t.Fatalf("spoken = %q, want a user cancel to stay silent", got)
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
