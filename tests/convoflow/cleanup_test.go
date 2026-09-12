package main

import (
	"context"
	"errors"
	"testing"
)

type failingCleanupRunner struct {
	*deterministicWorkRunner
	cleanupErr           error
	closeFailure         error
	closed               bool
	lastExportAfterClose bool
}

func (r *failingCleanupRunner) validatePersonaRun() error { return r.cleanupErr }
func (r *failingCleanupRunner) close() {
	r.closed = true
	r.cleanupErr = r.closeFailure
}
func (r *failingCleanupRunner) writePersonaArtifacts(string) error {
	r.lastExportAfterClose = r.closed
	return nil
}

func TestEngineReportsFinalCheckpointFailure(t *testing.T) {
	rec := &recorder{}
	r := &failingCleanupRunner{deterministicWorkRunner: newDeterministicWorkRunner(rec, false)}
	writeFailure := errors.New("final checkpoint disk failure")
	r.closeFailure = writeFailure
	logClosedAfterRunner := false
	e := &engine{scn: &Scenario{Name: "cleanup"}, runDir: t.TempDir(), rec: rec, runner: r,
		onClose: []func(){func() { logClosedAfterRunner = r.closed }}}
	if err := e.run(context.Background()); !errors.Is(err, writeFailure) {
		t.Fatalf("run after failing final checkpoint = %v, want %v", err, writeFailure)
	}
	if !r.lastExportAfterClose || !logClosedAfterRunner {
		t.Fatalf("finalization order: exported after runner=%t, log closed after runner=%t", r.lastExportAfterClose, logClosedAfterRunner)
	}
}
