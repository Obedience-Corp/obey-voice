package work

import (
	"errors"
	"testing"
)

func communicatedFixture(t *testing.T, limit int) (*Coordinator, WorkScope, ResultView) {
	t.Helper()
	scope := testScope()
	c := NewCoordinator(scope, WithPendingLimit(limit))
	k, err := c.Register(scope, Admission{JobID: "replay-job", Task: "replay task", Provider: "local", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Publish(k, Outcome{Scope: scope, JobID: k.JobID, Status: OutcomeSucceeded, Text: "retained result"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.AcquireCapturePermit(scope, "owner", 1)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.Claim(scope, "old-attempt", "old-turn", []ResultRef{{Scope: scope, JobID: k.JobID, ResultID: v.Outcome.ResultID, Revision: v.Outcome.TerminalRevision}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.FinishAttempt(l, []string{v.Outcome.ResultID}, DeliveryReceipt{Scope: scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: 1, Mode: "text", ResponseCommitted: true}); err != nil {
		t.Fatal(err)
	}
	return c, scope, c.Snapshot(scope).Communicated[0]
}

func TestReplayReAdmitsCommunicatedResultWithoutProviderRun(t *testing.T) {
	c, scope, old := communicatedFixture(t, 4)
	if err := c.Replay(scope, old.Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
	pending := c.Pending(scope)
	if len(pending) != 1 || pending[0].State != Ready || pending[0].Outcome.ResultID != old.Outcome.ResultID || pending[0].Task != old.Task {
		t.Fatalf("replayed pending=%+v", pending)
	}
	if len(c.Snapshot(scope).Communicated) != 0 {
		t.Fatal("replayed result remained communicated")
	}
	if got := c.Pending(scope)[0].Outcome; !samePersistedOutcome(got, old.Outcome) {
		t.Fatalf("replay mutated immutable outcome: got=%+v want=%+v", got, old.Outcome)
	}
	if got := c.Pending(scope)[0].LastAttempt; got != nil {
		t.Fatalf("replay retained stale delivery evidence: %+v", got)
	}
	if err := c.Replay(scope, old.Outcome.ResultID); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate replay error=%v", err)
	}
	if _, err := c.Register(scope, Admission{JobID: old.Outcome.JobID}); err == nil {
		t.Fatal("terminal replay job was readmitted")
	}
	p, err := c.AcquireCapturePermit(scope, "owner-new", 2)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.Claim(scope, "new-attempt", "new-turn", []ResultRef{{Scope: scope, JobID: old.Outcome.JobID, ResultID: old.Outcome.ResultID, Revision: old.Outcome.TerminalRevision}}, p)
	if err != nil {
		t.Fatal(err)
	}
	oldReceipt := DeliveryReceipt{Scope: scope, AttemptID: "old-attempt", TurnID: "old-turn", CaptureEpoch: 1, Mode: "text", ResponseCommitted: true}
	if err := c.FinishAttempt(l, []string{old.Outcome.ResultID}, oldReceipt); !errors.Is(err, ErrConflict) {
		t.Fatalf("old receipt accepted for new attempt: %v", err)
	}
	if err := c.FinishAttempt(l, []string{old.Outcome.ResultID}, DeliveryReceipt{Scope: scope, AttemptID: "new-attempt", TurnID: "new-turn", CaptureEpoch: 2, Mode: "text", ResponseCommitted: true}); err != nil {
		t.Fatal(err)
	}
}

func TestReplayRejectsStaleUnknownAndOverflow(t *testing.T) {
	c, scope, old := communicatedFixture(t, 2)
	before := c.Snapshot(scope)
	if err := c.Replay(WorkScope{SessionID: scope.SessionID, RuntimeEpoch: "other", ConversationEpoch: scope.ConversationEpoch}, old.Outcome.ResultID); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale scope error=%v", err)
	}
	if err := c.Replay(scope, "missing"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown error=%v", err)
	}
	for i := 0; i < 2; i++ {
		k, err := c.Register(scope, Admission{JobID: "still-pending-" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Publish(k, Outcome{Scope: scope, JobID: k.JobID, Status: OutcomeSucceeded, Text: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Replay(scope, old.Outcome.ResultID); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
	after := c.Snapshot(scope)
	if len(after.Communicated) != len(before.Communicated) || len(after.Pending) != 2 {
		t.Fatalf("failed replay mutated state: before=%+v after=%+v", before, after)
	}
}

func TestReplayRestoredCommunicatedResultIsHeldUntilExplicitReplay(t *testing.T) {
	c, source, old := communicatedFixture(t, 4)
	snap := c.Snapshot(source)
	destination := WorkScope{SessionID: source.SessionID, RuntimeEpoch: "restored-runtime", ConversationEpoch: "restored-conversation"}
	d := NewCoordinator(source, WithPendingLimit(4))
	if err := d.Restore(snap, destination); err != nil {
		t.Fatalf("restore=%v snapshot=%+v", err, snap)
	}
	if got := d.Pending(destination); len(got) != 0 {
		t.Fatalf("restored communicated result became pending: %+v", got)
	}
	if err := d.Replay(destination, old.Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
	if got := d.Pending(destination); len(got) != 1 || got[0].State != Ready || got[0].Outcome.PreviousSession != old.Outcome.PreviousSession {
		t.Fatalf("restored replay=%+v", got)
	}
}
