package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// outOfTimeWorker either reports that its answer was cut off or, with no
// error to report, waits for the job to stop it.
type outOfTimeWorker struct{ err error }

func (*outOfTimeWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *outOfTimeWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	<-ctx.Done()
	return "", ctx.Err()
}
func (*outOfTimeWorker) ClearHistory()            {}
func (*outOfTimeWorker) History() []brain.Turn    { return nil }
func (*outOfTimeWorker) LoadHistory([]brain.Turn) {}

// Whether the worker cuts its own answer off or the job budget stops it first,
// the result says the time ran out before the answer was written.
func TestWorkerOutOfTimeOutcome(t *testing.T) {
	const partial = "Two of the three files were read."
	for _, tc := range []struct {
		name   string
		budget time.Duration
		worker *outOfTimeWorker
		want   []string
	}{
		{"worker cut its answer off", time.Minute, &outOfTimeWorker{err: &brain.WorkerOutOfTimeError{Partial: partial}}, []string{"Partial answer: " + partial}},
		{"job budget stopped it", 100 * time.Millisecond, &outOfTimeWorker{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := job.NewTable(context.Background(), job.WithBudget(tc.budget))
			t.Cleanup(jobs.Close)
			p := &Pipeline{Events: events.NewBus()}
			limits := make(chan events.WorkerLimitReached, 1)
			events.Subscribe(p.Events, func(e events.WorkerLimitReached) { limits <- e })
			cfg := &config.Config{}
			a := NewAgency(p, jobs, tc.worker, cfg, nil)
			a.Wire()
			scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
			c := work.NewCoordinator(scope)
			if _, err := c.Register(scope, work.Admission{JobID: "timed-job", Task: "read the files"}); err != nil {
				t.Fatal(err)
			}
			a.workMu.Lock()
			a.coordinator, a.workScope = c, scope
			a.jobScopes["timed-job"] = scope
			a.workMu.Unlock()

			if _, err := jobs.Start(context.Background(), job.Spec{ID: "timed-job", Task: "read the files", Run: a.workerRun("read the files", tc.worker, cfg)}); err != nil {
				t.Fatal(err)
			}
			var outcome work.Outcome
			deadline := time.Now().Add(5 * time.Second)
			for {
				if pending := c.Pending(scope); len(pending) > 0 {
					outcome = pending[0].Outcome
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no outcome was published")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if outcome.Status != work.OutcomeTimedOut {
				t.Fatalf("status = %q, want %q (text %q)", outcome.Status, work.OutcomeTimedOut, outcome.Text)
			}
			for _, want := range append([]string{"Work ran out of time after", "before it could write its answer."}, tc.want...) {
				if !strings.Contains(outcome.Text, want) {
					t.Fatalf("text = %q, want it to contain %q", outcome.Text, want)
				}
			}
			if strings.Contains(outcome.Text, "deadline exceeded") {
				t.Fatalf("text = %q", outcome.Text)
			}
			select {
			case <-limits:
			case <-time.After(time.Second):
				t.Fatal("the time limit was not reported as a worker limit")
			}
		})
	}
}

// Without the work inbox the cut is reported as a failed job naming the cause.
func TestLegacyWorkerOutOfTimeFailureNamesTheCause(t *testing.T) {
	jobs := job.NewTable(context.Background(), job.WithBudget(time.Minute))
	t.Cleanup(jobs.Close)
	p := &Pipeline{Events: events.NewBus()}
	failed := make(chan events.JobFailed, 1)
	events.Subscribe(p.Events, func(e events.JobFailed) { failed <- e })
	worker := &outOfTimeWorker{err: &brain.WorkerOutOfTimeError{}}
	cfg := &config.Config{}
	a := NewAgency(p, jobs, worker, cfg, nil)
	a.Wire()
	if _, err := jobs.Start(context.Background(), job.Spec{Task: "read the files", Run: a.workerRun("read the files", worker, cfg)}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Err, "ran out of time before it could write its answer") {
			t.Fatalf("job failure = %q", e.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job did not fail")
	}
}
