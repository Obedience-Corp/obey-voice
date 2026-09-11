package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type lifecycleWorker struct{ reply string }

func (w *lifecycleWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *lifecycleWorker) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return w.reply, nil
}
func (w *lifecycleWorker) ClearHistory()            {}
func (w *lifecycleWorker) History() []brain.Turn    { return nil }
func (w *lifecycleWorker) LoadHistory([]brain.Turn) {}

func TestWorkAgencyClearFencesLateTerminalCallback(t *testing.T) {
	jobs := job.NewTable(context.Background())
	p := &Pipeline{Events: events.NewBus()}
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "done"}, &config.Config{}, nil)
	a.Wire()
	if err := a.EnableConversationWork("session", nil, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	c := a.WorkCoordinator()
	old := a.WorkSnapshot().Scope
	key, err := c.Register(old, work.Admission{JobID: "stable-job", Task: "task", Origin: "agency"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if got := a.WorkSnapshot().Scope; got == old {
		t.Fatal("clear did not advance conversation scope")
	}
	if _, err := c.Publish(key, work.Outcome{Scope: old, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "late"}); !errors.Is(err, work.ErrStaleScope) {
		t.Fatalf("late publish = %v", err)
	}
}

func TestWorkAgencyRestoresPendingAsHeldInFreshScope(t *testing.T) {
	jobs := job.NewTable(context.Background())
	p := &Pipeline{Events: events.NewBus()}
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "done"}, &config.Config{}, nil)
	a.Wire()
	if err := a.EnableConversationWork("session", nil, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	c := a.WorkCoordinator()
	source := a.WorkSnapshot().Scope
	key, _ := c.Register(source, work.Admission{JobID: "job-1", Task: "task", Origin: "agency"})
	_, _ = c.Publish(key, work.Outcome{Scope: source, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "done"})
	saved := a.WorkSnapshot()
	if err := a.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if err := a.EnableConversationWork("session", &saved, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	got := a.WorkCoordinator().Pending(a.WorkSnapshot().Scope)
	if len(got) != 1 || got[0].State != work.Held || !got[0].Outcome.PreviousSession {
		t.Fatalf("restored pending = %+v", got)
	}
	if got[0].DeliveryKey.Scope == source {
		t.Fatal("restored delivery retained stale scope")
	}
}
