package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func workSnapshotEvent(scope work.WorkScope, revision uint64, key work.JobKey, state work.JobState, status work.OutcomeStatus) events.WorkStateChanged {
	job := work.JobView{Key: key, State: state, Outcome: nil}
	if state == work.JobTerminal {
		job.Outcome = &work.Outcome{
			Scope:  scope,
			JobID:  key.JobID,
			Status: status,
			Config: map[string]string{"persona": "samantha"},
		}
	}
	return events.WorkStateChanged{Snapshot: work.Snapshot{
		Scope:    scope,
		Revision: revision,
		Jobs:     []work.JobView{job},
	}}
}

func TestCanonicalWorkEventsDeduplicateByScopeAndJob(t *testing.T) {
	scopeA := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "a"}
	scopeB := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "b"}
	rec := &recorder{}
	keyA := work.JobKey{Scope: scopeA, JobID: "same-id"}
	keyB := work.JobKey{Scope: scopeB, JobID: "same-id"}

	// The duplicate running and terminal snapshots are the normal observer
	// pattern: persistence and UI can publish the same immutable projection
	// more than once. A same-named job in another scope is a separate lifecycle.
	for _, e := range []events.WorkStateChanged{
		workSnapshotEvent(scopeA, 1, keyA, work.JobQueued, ""),
		workSnapshotEvent(scopeA, 2, keyA, work.JobRunning, ""),
		workSnapshotEvent(scopeA, 3, keyA, work.JobRunning, ""),
		workSnapshotEvent(scopeA, 4, keyA, work.JobTerminal, work.OutcomeSucceeded),
		workSnapshotEvent(scopeA, 5, keyA, work.JobTerminal, work.OutcomeSucceeded),
		workSnapshotEvent(scopeA, 2, keyA, work.JobRunning, ""), // delayed old publication
		workSnapshotEvent(scopeA, 5, keyA, work.JobTerminal, work.OutcomeSucceeded),
		workSnapshotEvent(scopeB, 1, keyB, work.JobQueued, ""),
		workSnapshotEvent(scopeB, 2, keyB, work.JobRunning, ""),
		workSnapshotEvent(scopeB, 3, keyB, work.JobTerminal, work.OutcomePartial),
	} {
		rec.add(e)
	}

	m := collectMetrics(rec)
	if m.JobStarted != 2 || m.JobFinished != 2 || m.JobFailed != 0 || m.JobAborted != 0 {
		t.Fatalf("canonical lifecycle metrics = %+v, want starts=2 finished=2 failed=0 aborted=0", m)
	}
	for _, name := range []string{"job_queued", "job_started", "job_terminal", "job_finished"} {
		if !rec.hasEvent(name) {
			t.Errorf("canonical snapshot did not satisfy %q", name)
		}
	}
}

func TestCanonicalWorkFailureAndAbortDoNotSatisfyFinished(t *testing.T) {
	tests := []struct {
		name   string
		status work.OutcomeStatus
		failed int
		abort  int
		event  string
	}{
		{name: "failed", status: work.OutcomeFailed, failed: 1, event: "job_failed"},
		{name: "timed out", status: work.OutcomeTimedOut, failed: 1, event: "job_failed"},
		{name: "cancelled", status: work.OutcomeCancelled, abort: 1, event: "job_aborted"},
		{name: "interrupted", status: work.OutcomeInterrupted, abort: 1, event: "job_aborted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: tc.name}
			key := work.JobKey{Scope: scope, JobID: "job"}
			rec := &recorder{}
			rec.add(workSnapshotEvent(scope, 1, key, work.JobQueued, ""))
			rec.add(workSnapshotEvent(scope, 2, key, work.JobRunning, ""))
			rec.add(workSnapshotEvent(scope, 3, key, work.JobTerminal, tc.status))
			rec.add(workSnapshotEvent(scope, 4, key, work.JobTerminal, tc.status))

			m := collectMetrics(rec)
			if m.JobStarted != 1 || m.JobFinished != 0 || m.JobFailed != tc.failed || m.JobAborted != tc.abort {
				t.Fatalf("metrics = %+v, want start=1 finish=0 failed=%d aborted=%d", m, tc.failed, tc.abort)
			}
			if !rec.hasEvent(tc.event) || !rec.hasEvent("job_terminal") {
				t.Fatalf("canonical %s status did not satisfy terminal predicate", tc.name)
			}
			if rec.hasEvent("job_finished") {
				t.Fatal("failure/abort status incorrectly satisfied job_finished")
			}
		})
	}
}

func TestWorkReceiptRequiresAudibleVoiceDelivery(t *testing.T) {
	base := work.DeliveryReceipt{Mode: "voice", ResponseCommitted: true, PlaybackComplete: true, Audible: true, Segments: 1}
	tests := []struct {
		name    string
		receipt work.DeliveryReceipt
		want    bool
	}{
		{name: "complete voice", receipt: base, want: true},
		{name: "text mode", receipt: func() work.DeliveryReceipt { r := base; r.Mode = "text"; return r }()},
		{name: "interrupted", receipt: func() work.DeliveryReceipt { r := base; r.Interrupted = true; return r }()},
		{name: "not committed", receipt: func() work.DeliveryReceipt { r := base; r.ResponseCommitted = false; return r }()},
		{name: "playback incomplete", receipt: func() work.DeliveryReceipt { r := base; r.PlaybackComplete = false; return r }()},
		{name: "inaudible", receipt: func() work.DeliveryReceipt { r := base; r.Audible = false; return r }()},
		{name: "no segments", receipt: func() work.DeliveryReceipt { r := base; r.Segments = 0; return r }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := work.Snapshot{Communicated: []work.ResultView{{LastAttempt: &work.DeliveryEvidence{Receipt: tc.receipt}}}}
			if got := workHasReceipt(snapshot); got != tc.want {
				t.Fatalf("workHasReceipt() = %v, want %v for %#v", got, tc.want, tc.receipt)
			}
		})
	}
}

type gatedAgencyWorker struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (w *gatedAgencyWorker) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("gated worker stream path is not used")
}

func (w *gatedAgencyWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.release:
		return "created worker-proof.txt", w.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (*gatedAgencyWorker) ClearHistory()            {}
func (*gatedAgencyWorker) History() []brain.Turn    { return nil }
func (*gatedAgencyWorker) LoadHistory([]brain.Turn) {}

func TestAgencyCanonicalWorkEventsWithGatedProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	worker := &gatedAgencyWorker{started: make(chan struct{}), release: make(chan struct{})}
	rec := &recorder{}
	bus := events.NewBus()
	bus.SubscribeAll(rec.add)
	cfg := &config.Config{
		VoiceWorkEnabled:         true,
		VoiceWorkerProvider:      "ollama",
		VoiceWorkerModel:         "qwen3:8b",
		VoiceWorkerMaxConcurrent: 1,
		ActivePersona:            "samantha",
		VoiceToolsEnabled:        true,
		VoiceRouterTools:         "route_only",
		VoiceWorkerJobBudget:     30,
		WorkDir:                  t.TempDir(),
	}
	live := &deterministicBrain{persona: "Samantha"}
	agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{
		Config:   cfg,
		Events:   bus,
		Brain:    live,
		Worker:   worker,
		TextOnly: true,
		Silent:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := agent.Pipeline.EnableConversationWork("canonical-events", nil, cfg); err != nil {
		t.Fatal(err)
	}

	turnDone := make(chan error, 1)
	go func() {
		turnDone <- agent.RunTurnTextMode(ctx, "Create a file called worker-proof.txt in the current working directory containing exactly blue comet. Do not inspect or modify any other files. Confirm completion.")
	}()
	select {
	case <-worker.started:
	case <-ctx.Done():
		t.Fatalf("worker did not start: %v", ctx.Err())
	}
	if err := rec.waitFor(ctx, waitJobStarted, nil); err != nil {
		t.Fatal(err)
	}
	for _, te := range rec.snapshot() {
		if _, legacy := te.event.(events.JobStarted); legacy {
			t.Fatal("canonical work emitted legacy JobStarted")
		}
	}
	close(worker.release)
	if err := rec.waitFor(ctx, waitJobTerminal, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-turnDone; err != nil {
		t.Fatalf("routed turn: %v", err)
	}

	m := collectMetrics(rec)
	if m.JobStarted != 1 || m.JobFinished != 1 || m.JobFailed != 0 || m.JobAborted != 0 {
		t.Fatalf("canonical Agency metrics = %+v, want start=1 finish=1 failed=0 aborted=0", m)
	}
	for _, te := range rec.snapshot() {
		if _, legacy := te.event.(events.JobFinished); legacy {
			t.Fatal("canonical work emitted legacy JobFinished")
		}
	}
	snapshot := agent.Pipeline.WorkSnapshot()
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].Outcome == nil {
		t.Fatalf("final canonical snapshot = %+v, want one terminal job", snapshot)
	}
	job := snapshot.Jobs[0]
	if job.State != work.JobTerminal || job.Outcome.Status != work.OutcomeSucceeded {
		t.Fatalf("terminal job = %+v, want succeeded terminal", job)
	}
	if got := job.Outcome.Config["persona"]; got != "samantha" {
		t.Fatalf("frozen origin persona = %q, want samantha", got)
	}
}
