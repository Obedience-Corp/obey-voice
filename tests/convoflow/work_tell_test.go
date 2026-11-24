package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// tellingRunner corrects a job without any model: the correction is taken,
// carried by the next request, and the job finishes having written the file.
type tellingRunner struct {
	fakeRunner
	workDir string
	told    []string
}

func (r *tellingRunner) tellWork(_ context.Context, text string) error {
	r.told = append(r.told, text)
	r.rec.add(events.JobCorrection{ID: "job-1", Revision: 1, Text: text, State: "pending"})
	r.rec.add(events.JobCorrection{ID: "job-1", Revision: 1, Text: text, State: "included"})
	if err := os.MkdirAll(filepath.Join(r.workDir, "job-1"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.workDir, "job-1", "gamma.txt"), []byte("corrected\n"), 0o600); err != nil {
		return err
	}
	r.rec.add(events.JobFinished{ID: "job-1", Summary: "Wrote alpha.txt, beta.txt and gamma.txt."})
	return nil
}

func TestEngineWorkTellTurn(t *testing.T) {
	scn, err := loadScenario(filepath.Join("scenarios", "work-stream-tell.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The spawn turn needs a model, and nothing plays here to score.
	scn.Turns = scn.Turns[1:]
	scn.Expect = scn.Expect[:4]
	rec := &recorder{}
	rec.add(events.JobQueued{ID: "job-1"})
	rec.add(events.JobStarted{ID: "job-1"})
	workDir := t.TempDir()
	runner := &tellingRunner{fakeRunner: fakeRunner{rec: rec}, workDir: workDir}
	eng := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: runner, profile: runProfile{WorkDir: workDir}}
	if err := eng.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runner.told) != 1 || !strings.Contains(runner.told[0], "gamma.txt") || len(runner.hist) != 0 {
		t.Fatalf("told=%q history=%v", runner.told, runner.hist)
	}
}

func TestEngineWorkTellNeedsATeller(t *testing.T) {
	rec := &recorder{}
	eng := &engine{
		scn:    &Scenario{Name: "t", Turns: []Turn{{ID: "tell", Op: "work_tell", User: "Stop at two.", Wait: waitIdle}}},
		runDir: t.TempDir(), rec: rec, runner: &fakeRunner{rec: rec},
	}
	if err := eng.run(t.Context()); err == nil || !strings.Contains(err.Error(), "does not support work corrections") {
		t.Fatalf("err=%v", err)
	}
}

func TestCorrectionAndWorkFileExpectations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gamma.txt"), []byte("uncorrected"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := turnState{workDir: dir, events: []timedEvent{{event: events.JobCorrection{ID: "job-1", State: "pending"}}}}
	if err := evalExpect(Expectation{Correction: "pending"}, &recorder{}, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{Correction: "included"}, &recorder{}, st); err == nil {
		t.Fatal("a correction that was only taken passed as included")
	}
	if err := evalExpect(Expectation{Event: "job_correction"}, &recorder{}, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{WorkFile: "gamma.txt"}, &recorder{}, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{WorkFile: "gamma.txt", WorkFileContains: "corrected\n"}, &recorder{}, st); err == nil {
		t.Fatal("the wrong contents passed")
	}
	if err := evalExpect(Expectation{WorkFile: "third.txt"}, &recorder{}, st); err == nil {
		t.Fatal("a missing file passed")
	}
	if err := evalExpect(Expectation{WorkFile: "gamma.txt"}, &recorder{}, turnState{}); err == nil {
		t.Fatal("a run without a work directory passed")
	}
}

func TestTellNewestJob(t *testing.T) {
	t.Parallel()
	now := time.Now()
	workers := []pipeline.WorkerInfo{
		{ID: "job-1", State: "running", Started: now.Add(-time.Minute)},
		{ID: "job-2", State: "running", Started: now},
		{ID: "job-3", State: "finished", Started: now.Add(time.Second)},
	}
	var got string
	accept := func(id, text string) pipeline.WorkReceipt {
		got = id
		return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: pipeline.ControlAccepted, Revision: 1}}}
	}
	if err := tellNewestJob(workers, accept, "Stop at two."); err != nil || got != "job-2" {
		t.Fatalf("told %q err=%v", got, err)
	}
	finished := func(id, text string) pipeline.WorkReceipt {
		return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: pipeline.ControlFinished}}}
	}
	if err := tellNewestJob(workers, finished, "Stop at two."); err == nil {
		t.Fatal("a correction the job never took passed")
	}
	rejected := func(id, text string) pipeline.WorkReceipt {
		return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: pipeline.ControlRejected, Reason: "the job is finishing"}}}
	}
	if err := tellNewestJob(workers, rejected, "Stop at two."); err == nil || !strings.Contains(err.Error(), "finishing") {
		t.Fatalf("err=%v", err)
	}
	if err := tellNewestJob(workers[2:], accept, "Stop at two."); err == nil {
		t.Fatal("a tell with no running job passed")
	}
}

func TestWorkTellScenarioChecks(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty correction": "name: t\nturns:\n  - { id: tell, op: work_tell }\n",
		"contents alone":   "name: t\nturns:\n  - { id: tell, op: work_tell, user: x, expect: [ { work_file_contains: corrected } ] }\n",
	} {
		if _, err := loadScenarioString(name, body); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestWaitWorkerTool(t *testing.T) {
	t.Parallel()
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	rec := &recorder{}
	rec.add(events.ToolCallFinished{JobID: "job-0", Name: "write_file"})
	mark := len(rec.snapshot())
	rec.add(events.JobStarted{ID: "job-1"})
	rec.add(events.ToolCallFinished{Name: "route"})
	if err := rec.waitWorkerTool(short(), mark); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call before the turn or the live brain's own ended the wait: %v", err)
	}
	go rec.add(events.ToolCallFinished{JobID: "job-1", Name: "write_file"})
	if err := rec.waitWorkerTool(t.Context(), mark); err != nil {
		t.Fatal(err)
	}

	ended := &recorder{}
	ended.add(events.JobStarted{ID: "job-1"})
	ended.add(events.JobFinished{ID: "job-1"})
	ended.add(events.ToolCallFinished{JobID: "job-2", Name: "write_file"})
	if err := ended.waitWorkerTool(short(), 0); err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatalf("err=%v", err)
	}
}

func TestEngineWaitsForTheWorkerTool(t *testing.T) {
	rec := &recorder{}
	runner := &fakeRunner{rec: rec, onTurn: func(string) {
		rec.add(events.JobStarted{ID: "job-1"})
		rec.add(events.ToolCallFinished{JobID: "job-1", Name: "write_file"})
	}}
	scn, err := loadScenarioString("t", "name: t\nturns:\n  - { id: spawn, user: write the files, wait: worker_tool }\n")
	if err != nil {
		t.Fatal(err)
	}
	eng := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: runner}
	if err := eng.run(t.Context()); err != nil {
		t.Fatal(err)
	}

	rec = &recorder{}
	runner = &fakeRunner{rec: rec, onTurn: func(string) { rec.add(events.JobFinished{ID: "job-1"}) }}
	eng = &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: runner}
	if err := eng.run(t.Context()); err == nil || !strings.Contains(err.Error(), "wait worker_tool") {
		t.Fatalf("a job that ended without a tool call passed: %v", err)
	}
}
