package main

import (
	"context"
	"errors"
	"testing"
)

type failingCleanupRunner struct {
	*deterministicWorkRunner
	cleanupErr error
}

func (r *failingCleanupRunner) validatePersonaRun() error { return r.cleanupErr }

func TestEngineReportsFinalCheckpointFailure(t *testing.T) {
	rec := &recorder{}
	r := &failingCleanupRunner{deterministicWorkRunner: newDeterministicWorkRunner(rec, false)}
	writeFailure := errors.New("final checkpoint disk failure")
	e := &engine{scn: &Scenario{Name: "cleanup"}, runDir: t.TempDir(), rec: rec, runner: r,
		onClose: []func(){func() { r.cleanupErr = writeFailure }}}
	if err := e.run(context.Background()); !errors.Is(err, writeFailure) {
		t.Fatalf("run after failing final checkpoint = %v, want %v", err, writeFailure)
	}
}
