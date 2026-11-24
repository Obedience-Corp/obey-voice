package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// reportingRunner asks a job for its report without any model: the report
// is accepted, the job finishes cut short, and its answer is spoken.
type reportingRunner struct {
	fakeRunner
	reports int
}

func (r *reportingRunner) reportWork(context.Context) error {
	r.reports++
	r.rec.add(events.JobReport{ID: "job-1", Checkpoint: "wrote 1 file", Answer: true})
	r.rec.add(events.JobFinished{ID: "job-1", CutShort: true, CutShortReason: "report requested", Summary: "Wrote one.txt; two.txt to five.txt are not written."})
	go r.rec.add(events.JobResultDelivered{ID: "job-1", Summary: "Wrote one.txt; two.txt to five.txt are not written.", Spoken: true})
	return nil
}

func TestEngineWorkReportTurn(t *testing.T) {
	scn, err := loadScenario(filepath.Join("scenarios", "work-stream-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The spawn turn needs a model, and nothing plays here to score.
	scn.Turns = scn.Turns[1:]
	scn.Expect = scn.Expect[:4]
	rec := &recorder{}
	rec.add(events.JobQueued{ID: "job-1"})
	rec.add(events.JobStarted{ID: "job-1"})
	runner := &reportingRunner{fakeRunner: fakeRunner{rec: rec}}
	eng := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: runner}
	if err := eng.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runner.reports != 1 || len(runner.hist) != 0 {
		t.Fatalf("reports=%d history=%v", runner.reports, runner.hist)
	}
}

func TestEngineWorkReportNeedsAReporter(t *testing.T) {
	rec := &recorder{}
	eng := &engine{
		scn:    &Scenario{Name: "t", Turns: []Turn{{ID: "report", Op: "work_report", Wait: waitIdle}}},
		runDir: t.TempDir(), rec: rec, runner: &fakeRunner{rec: rec},
	}
	if err := eng.run(t.Context()); err == nil || !strings.Contains(err.Error(), "does not support work reports") {
		t.Fatalf("err=%v", err)
	}
}

func TestCutShortExpectation(t *testing.T) {
	t.Parallel()
	finished := func(e events.JobFinished) turnState { return turnState{events: []timedEvent{{event: e}}} }
	if err := evalExpect(Expectation{CutShort: "report requested"}, &recorder{}, finished(events.JobFinished{ID: "job-1", CutShort: true, CutShortReason: "report requested", Summary: "Wrote one file."})); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]events.JobFinished{
		"not cut short":  {ID: "job-1", Summary: "Wrote every file."},
		"another reason": {ID: "job-1", CutShort: true, CutShortReason: "repeated the same tool call", Summary: "Stuck."},
		"no answer":      {ID: "job-1", CutShort: true, CutShortReason: "report requested"},
	} {
		if err := evalExpect(Expectation{CutShort: "report requested"}, &recorder{}, finished(e)); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	if err := evalExpect(Expectation{Event: "job_report"}, &recorder{}, turnState{events: []timedEvent{{event: events.JobReport{ID: "job-1"}}}}); err != nil {
		t.Fatal(err)
	}
	stop := true
	log := []timedEvent{
		{event: events.ToolCallStarted{JobID: "job-1", Name: "write_file"}},
		{event: events.JobReport{ID: "job-1"}},
		{event: events.ToolCallStarted{JobID: "job-2", Name: "write_file"}},
	}
	if err := evalExpect(Expectation{ToolsStopAfterReport: &stop}, &recorder{}, turnState{events: log}); err != nil {
		t.Fatal(err)
	}
	log = append(log, timedEvent{event: events.ToolCallStarted{JobID: "job-1", Name: "write_file"}})
	if err := evalExpect(Expectation{ToolsStopAfterReport: &stop}, &recorder{}, turnState{events: log}); err == nil {
		t.Fatal("a tool call that started after the report passed")
	}
	if err := evalExpect(Expectation{ToolsStopAfterReport: &stop}, &recorder{}, turnState{events: log[:1]}); err == nil {
		t.Fatal("a turn with no report passed")
	}
	still := false
	if err := evalExpect(Expectation{ToolsStopAfterReport: &still}, &recorder{}, turnState{events: log[:1]}); err == nil {
		t.Fatal("a turn with no report passed as tools still starting")
	}
	log = append(log, timedEvent{event: events.JobReport{ID: "job-1"}})
	if err := evalExpect(Expectation{ToolsStopAfterReport: &stop}, &recorder{}, turnState{events: log}); err == nil {
		t.Fatal("a later report hid a tool call that started after the first")
	}
}

func TestReportNewestJob(t *testing.T) {
	t.Parallel()
	now := time.Now()
	workers := []pipeline.WorkerInfo{
		{ID: "job-1", State: "running", Started: now.Add(-time.Minute)},
		{ID: "job-2", State: "running", Started: now},
		{ID: "job-3", State: "finished", Started: now.Add(time.Second)},
	}
	var got string
	receipt := func(outcome pipeline.ControlOutcome, reason string) func(string) pipeline.WorkReceipt {
		return func(id string) pipeline.WorkReceipt {
			got = id
			return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: id, Outcome: outcome, Reason: reason, Checkpoint: "wrote 1 file"}}}
		}
	}
	if err := reportNewestJob(workers, receipt(pipeline.ControlAccepted, "")); err != nil || got != "job-2" {
		t.Fatalf("reported %q err=%v", got, err)
	}
	if err := reportNewestJob(workers, receipt(pipeline.ControlRejected, "this worker cannot be asked to stop, so the work continues")); err == nil || !strings.Contains(err.Error(), "cannot be asked") {
		t.Fatalf("a checkpoint-only report passed: %v", err)
	}
	if err := reportNewestJob(workers, receipt(pipeline.ControlFinished, "")); err == nil {
		t.Fatal("a report to a finished job passed")
	}
	if err := reportNewestJob(workers[2:], receipt(pipeline.ControlAccepted, "")); err == nil {
		t.Fatal("a report with no running job passed")
	}
	empty := func(string) pipeline.WorkReceipt { return pipeline.WorkReceipt{} }
	if err := reportNewestJob(workers, empty); err == nil {
		t.Fatal("a receipt naming no job passed")
	}
	other := func(string) pipeline.WorkReceipt {
		return pipeline.WorkReceipt{Jobs: []pipeline.JobControl{{ID: "job-1", Outcome: pipeline.ControlAccepted}}}
	}
	if err := reportNewestJob(workers, other); err == nil {
		t.Fatal("a receipt for another job passed")
	}
}

func TestWaitReportSpoken(t *testing.T) {
	t.Parallel()
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	rec := &recorder{}
	rec.add(events.JobReport{ID: "job-1", Answer: true})
	rec.add(events.SpeechSegmentReady{Text: "I'll work on that and report back."})
	rec.add(events.JobResultDelivered{ID: "job-2", Summary: "Another job's answer.", Spoken: true})
	rec.add(events.JobResultDelivered{ID: "job-1", Summary: "Wrote one file."})
	if err := rec.waitFor(short(), waitReportSpoken, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("other speech, or the result unspoken, ended the wait: %v", err)
	}
	go rec.add(events.JobResultDelivered{ID: "job-1", Summary: "Wrote one file.", Spoken: true})
	if err := rec.waitFor(short(), waitReportSpoken, func() bool { return true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the wait ended while playback ran: %v", err)
	}
	if err := rec.waitFor(t.Context(), waitReportSpoken, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenarioString("t", "name: t\nturns:\n  - { id: answer, wait: report_spoken }\n"); err != nil {
		t.Fatal(err)
	}
}
