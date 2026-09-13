package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func actionPipeline(t *testing.T, tts bool) (*Pipeline, *work.Coordinator, work.WorkScope, string) {
	t.Helper()
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	p := &Pipeline{Events: events.NewBus()}
	if tts {
		p.TTS = &progressiveFakeTTS{}
		p.Player = newFakePlayer(0)
	}
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "unused"}, &config.Config{}, nil)
	a.Wire()
	if err := p.EnableConversationWork("actions", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	c := p.WorkCoordinator()
	scope := p.WorkSnapshot().Scope
	k, err := c.Register(scope, work.Admission{JobID: "action-job", Task: "action task"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Publish(k, work.Outcome{Scope: scope, JobID: k.JobID, Status: work.OutcomeSucceeded, Text: "action result"})
	if err != nil {
		t.Fatal(err)
	}
	return p, c, scope, v.Outcome.ResultID
}

func TestWorkResultActionKeepsHeldResultWhenOutputUnavailable(t *testing.T) {
	for _, name := range []string{"muted", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			p, c, scope, resultID := actionPipeline(t, name == "muted")
			if err := c.Hold(scope, resultID); err != nil {
				t.Fatal(err)
			}
			if name == "muted" {
				p.SetOutputMuted(true)
			}
			err := p.WorkResultAction(scope, resultID, "speak")
			if err == nil {
				t.Fatal("unavailable output action unexpectedly succeeded")
			}
			got := c.Pending(scope)
			if len(got) != 1 || got[0].State != work.Held {
				t.Fatalf("failed action changed held result: %+v", got)
			}
		})
	}
}

func TestWorkResultActionSpeakResumesWithoutRunningWorkerAndDismissRemoves(t *testing.T) {
	p, c, scope, resultID := actionPipeline(t, true)
	if err := c.Hold(scope, resultID); err != nil {
		t.Fatal(err)
	}
	if err := p.WorkResultAction(scope, resultID, "speak"); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].State != work.Ready {
		t.Fatalf("speak action did not ready result: %+v", got)
	}
	if err := p.WorkResultAction(scope, resultID, "dismiss"); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(scope); len(got) != 0 {
		t.Fatalf("dismiss retained result: %+v", got)
	}
	if err := p.WorkResultAction(work.WorkScope{SessionID: scope.SessionID, RuntimeEpoch: "stale", ConversationEpoch: scope.ConversationEpoch}, resultID, "speak"); !errors.Is(err, work.ErrStaleScope) {
		t.Fatalf("stale action error=%v", err)
	}
}
