package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failedInterruptionWorkerRunner struct {
	fakeRunner
	stops          int
	stopContextErr error
	stopDeadline   bool
}

func (r *failedInterruptionWorkerRunner) startLatencyWorker(context.Context, string) (string, error) {
	return "only-owned-job", nil
}
func (r *failedInterruptionWorkerRunner) stopLatencyWorker(ctx context.Context, id string) error {
	if id != "only-owned-job" {
		return errors.New("wrong job canceled")
	}
	r.stops++
	r.stopContextErr = ctx.Err()
	_, r.stopDeadline = ctx.Deadline()
	return nil
}
func (*failedInterruptionWorkerRunner) runAcousticInterruption(context.Context, string, string, string, string, interruptionStimulusOptions) error {
	return errors.New("physical control failed")
}
func TestInterruptionWorkloadEngineCleansWorkerOnFailedControl(t *testing.T) {
	rec := &recorder{}
	r := &failedInterruptionWorkerRunner{fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true}}
	e := &engine{scn: &Scenario{Name: "worker-control", Input: "speech", LatencyCondition: "worker", Turns: []Turn{
		{ID: "start", Op: "start_latency_worker", User: "bounded task"},
		{ID: "interruption", Op: "acoustic_interrupt", WAV: "initial", InterruptWAV: "interrupt", ExpectedTranscript: "known"},
		{ID: "stop", Op: "stop_latency_worker"},
	}}, runDir: t.TempDir(), rec: rec, runner: r, player: newRecordingPlayer(nil)}
	if err := e.run(t.Context()); err == nil {
		t.Fatal("failed control passed")
	}
	if r.stops != 1 || r.stopContextErr != nil || !r.stopDeadline {
		t.Fatalf("cleanup calls=%d context=%v bounded=%t", r.stops, r.stopContextErr, r.stopDeadline)
	}
	if len(e.latencySamples) != 0 {
		t.Fatal("control incorrectly entered ordinary voice collector")
	}
}
func TestInterruptionWorkloadEngineCleanupUsesFreshBoundedContext(t *testing.T) {
	rec := &recorder{}
	r := &failedInterruptionWorkerRunner{fakeRunner: fakeRunner{rec: rec}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e := &engine{scn: &Scenario{Name: "canceled", Duration: duration{time.Second}}, runDir: t.TempDir(), rec: rec, runner: r, latencyWorkerID: "only-owned-job"}
	_ = e.run(ctx)
	if r.stops != 1 || r.stopContextErr != nil || !r.stopDeadline {
		t.Fatalf("canceled cleanup calls=%d context=%v bounded=%t", r.stops, r.stopContextErr, r.stopDeadline)
	}
}
