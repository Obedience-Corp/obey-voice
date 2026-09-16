package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func validInterruptionWorkload() interruptionWorkloadReport {
	return interruptionWorkloadReport{Condition: "worker", JobID: "job", Scope: work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}, Provider: "ollama", Model: "model", Host: "http://127.0.0.1:11435", StartedAt: time.Now(), StimulusOffset: time.Second, StoppedOffset: 2 * time.Second, RunningAtStimulus: true, RunningAtStop: true, SuccessfulBeforeStimulus: 1, StoppedAndJoined: true, Requests: []interruptionWorkloadRequest{
		{StartedOffset: time.Millisecond, EndedOffset: 500 * time.Millisecond, Completed: true},
		{StartedOffset: 600 * time.Millisecond, EndedOffset: 2500 * time.Millisecond, Completed: true},
		{StartedOffset: 2600 * time.Millisecond, EndedOffset: 3 * time.Second}, // cancellation at cleanup
	}}
}
func TestInterruptionWorkloadRequiresRealProgressAndScopedJoin(t *testing.T) {
	if err := validateInterruptionWorkload(validInterruptionWorkload(), "worker"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*interruptionWorkloadReport){
		"canonical running only":     func(r *interruptionWorkloadReport) { r.Requests = nil; r.SuccessfulBeforeStimulus = 0 },
		"unfinished request":         func(r *interruptionWorkloadReport) { r.Requests[1].EndedOffset = 0; r.Requests[1].Completed = false },
		"stalled overlap canceled":   func(r *interruptionWorkloadReport) { r.Requests[1].Completed = false },
		"provider error":             func(r *interruptionWorkloadReport) { r.Requests[1].Failed = true },
		"work ended before stimulus": func(r *interruptionWorkloadReport) { r.Requests = r.Requests[:1] },
		"work started after stop":    func(r *interruptionWorkloadReport) { r.Requests[1].StartedOffset = 2100 * time.Millisecond },
		"not running at stimulus":    func(r *interruptionWorkloadReport) { r.RunningAtStimulus = false },
		"not running at stop":        func(r *interruptionWorkloadReport) { r.RunningAtStop = false },
		"not joined":                 func(r *interruptionWorkloadReport) { r.StoppedAndJoined = false },
		"wrong completed count":      func(r *interruptionWorkloadReport) { r.SuccessfulBeforeStimulus = 2 },
		"scope missing":              func(r *interruptionWorkloadReport) { r.Scope = work.WorkScope{} },
		"clock reversed":             func(r *interruptionWorkloadReport) { r.StoppedOffset = r.StimulusOffset },
		"different condition":        func(r *interruptionWorkloadReport) { r.Condition = "baseline" },
		"missing identity":           func(r *interruptionWorkloadReport) { r.Model = "" },
		"overflow":                   func(r *interruptionWorkloadReport) { r.Error = "provider request evidence overflow" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := validInterruptionWorkload()
			mut(&r)
			if err := validateInterruptionWorkload(r, "worker"); err == nil {
				t.Fatal("invalid evidence passed")
			}
		})
	}
	baseline := interruptionWorkloadReport{Condition: "baseline", Scope: validInterruptionWorkload().Scope, StartedAt: time.Now(), StimulusOffset: time.Second, StoppedOffset: 2 * time.Second}
	if err := validateInterruptionWorkload(baseline, "baseline"); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*interruptionWorkloadReport){func(r *interruptionWorkloadReport) { r.JobID = "other" }, func(r *interruptionWorkloadReport) { r.RunningAtStop = true }, func(r *interruptionWorkloadReport) { r.Requests = validInterruptionWorkload().Requests }} {
		bad := baseline
		mut(&bad)
		if err := validateInterruptionWorkload(bad, "baseline"); err == nil {
			t.Fatal("baseline worker activity passed")
		}
	}
}

type interruptionBlockingWorker struct {
	sustainedFakeProvider
	count  atomic.Int32
	second chan struct{}
}

func (p *interruptionBlockingWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	if p.count.Add(1) == 1 {
		return "successful actual provider result", nil
	}
	close(p.second)
	<-ctx.Done()
	return "", ctx.Err()
}
func TestInterruptionWorkloadFreshSharesEvidenceAndJoinsCancellation(t *testing.T) {
	inner := &interruptionBlockingWorker{second: make(chan struct{})}
	provider, err := newSustainedLocalWorker(&config.Config{VoiceWorkerProvider: "ollama"}, inner, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := provider.(*sustainedLocalWorker)
	e := w.evidence
	scope := validInterruptionWorkload().Scope
	if err := e.begin(scope); err != nil {
		t.Fatal(err)
	}
	if err := e.bind(work.JobKey{Scope: scope, JobID: "job"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := w.Fresh().ThinkFull(ctx, "private content", brain.StreamOptions{}); done <- err }()
	select {
	case <-inner.second:
	case <-time.After(time.Second):
		t.Fatal("second provider request not reached")
	}
	snap, active := e.snapshot()
	if active != 1 || len(snap.Requests) != 2 || !snap.Requests[0].Completed || snap.Requests[1].EndedOffset != 0 {
		t.Fatalf("live evidence=%+v active=%d", snap, active)
	}
	if err := e.begin(scope); err == nil {
		t.Fatal("reset allowed while provider active")
	}
	if err := e.markJoined("job"); err == nil {
		t.Fatal("active provider counted as joined")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not join")
	}
	if err := e.markJoined("other"); err == nil {
		t.Fatal("wrong job joined")
	}
	if err := e.markJoined("job"); err != nil {
		t.Fatal(err)
	}
	snap, active = e.snapshot()
	if active != 0 || !snap.StoppedAndJoined || snap.Requests[1].EndedOffset <= snap.Requests[1].StartedOffset || snap.Requests[1].Completed || snap.Requests[1].Failed {
		t.Fatalf("canceled evidence=%+v active=%d", snap, active)
	}
}

func TestInterruptionWorkloadStopIsScopedIdempotentAndPreservesOtherResults(t *testing.T) {
	p, _, _ := newCompletionBoundaryPipeline(t)
	scope := p.WorkSnapshot().Scope
	c := p.WorkCoordinator()
	keys := make([]work.JobKey, 0, 2)
	for _, task := range []string{"owned", "unrelated"} {
		key, err := c.Register(scope, work.Admission{Task: task, Provider: "ollama", Model: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Started(key); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: task}); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	evidence := &workloadEvidence{}
	if err := evidence.begin(scope); err != nil {
		t.Fatal(err)
	}
	if err := evidence.bind(keys[0]); err != nil {
		t.Fatal(err)
	}
	r := &liveRunner{agent: &voiceagent.Agent{Pipeline: p}, worker: &observing{inner: &sustainedLocalWorker{evidence: evidence}}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := r.stopLatencyWorker(ctx, keys[1].JobID); err == nil {
		t.Fatal("wrong job cleanup accepted")
	}
	for range 2 {
		if err := r.stopLatencyWorker(ctx, keys[0].JobID); err != nil {
			t.Fatal(err)
		}
	}
	pending := p.WorkSnapshot().Pending
	if len(pending) != 1 || pending[0].Outcome.JobID != keys[1].JobID {
		t.Fatalf("unrelated outcome altered: %+v", pending)
	}
}

func TestInterruptionWorkloadStopDoesNotClaimUnjoinedProvider(t *testing.T) {
	p, _, _ := newCompletionBoundaryPipeline(t)
	scope := p.WorkSnapshot().Scope
	c := p.WorkCoordinator()
	key, err := c.Register(scope, work.Admission{Task: "owned", Provider: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeCancelled}); err != nil {
		t.Fatal(err)
	}
	evidence := &workloadEvidence{}
	if err := evidence.begin(scope); err != nil {
		t.Fatal(err)
	}
	if err := evidence.bind(key); err != nil {
		t.Fatal(err)
	}
	finish := evidence.loopStarted()
	defer finish()
	r := &liveRunner{agent: &voiceagent.Agent{Pipeline: p}, worker: &observing{inner: &sustainedLocalWorker{evidence: evidence}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.stopLatencyWorker(ctx, key.JobID); !errors.Is(err, context.Canceled) {
		t.Fatalf("unjoined provider cleanup: %v", err)
	}
	snapshot, _ := evidence.snapshot()
	if snapshot.StoppedAndJoined {
		t.Fatal("terminal job masked live provider")
	}
}
