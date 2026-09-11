package work

import (
	"errors"
	"testing"
)

func completionFixture(t *testing.T, n int) (*Coordinator, WorkScope, CapturePermit, CompletionLease, []ResultRef) {
	t.Helper()
	scope := testScope()
	c := NewCoordinator(scope, WithPendingLimit(8))
	refs := make([]ResultRef, 0, n)
	for i := 0; i < n; i++ {
		k, err := c.Register(scope, Admission{Task: "task"})
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Publish(k, Outcome{Scope: scope, JobID: k.JobID, Status: OutcomeSucceeded, Text: k.JobID})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ResultRef{Scope: scope, JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision})
	}
	p, err := c.AcquireCapturePermit(scope, "test", 12)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.Claim(scope, "attempt-1", "turn-1", refs, p)
	if err != nil {
		t.Fatal(err)
	}
	return c, scope, p, l, refs
}

func TestFinishAttemptRequiresTruthfulReceiptAndCommunicatesSubset(t *testing.T) {
	c, scope, _, lease, refs := completionFixture(t, 2)
	if got := c.Pending(scope); len(got) != 2 || got[0].AnnouncementState != AnnouncementScheduled {
		t.Fatalf("claim projection = %+v", got)
	}
	bad := DeliveryReceipt{Scope: scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: "voice", ResponseCommitted: true, Audible: true, Segments: 0, PlaybackComplete: false}
	if err := c.FinishAttempt(lease, []string{refs[0].ResultID}, bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete voice receipt = %v", err)
	}
	if got := c.Pending(scope); len(got) != 2 {
		t.Fatalf("failed receipt consumed work: %+v", got)
	}
	good := bad
	good.PlaybackComplete, good.Segments = true, 1
	if err := c.FinishAttempt(lease, []string{refs[0].ResultID}, good); err != nil {
		t.Fatal(err)
	}
	pending := c.Pending(scope)
	if len(pending) != 1 || pending[0].Outcome.ResultID != refs[1].ResultID || pending[0].AnnouncementState != AnnouncementPending {
		t.Fatalf("omitted result = %+v", pending)
	}
	s := c.Snapshot(scope)
	if len(s.Communicated) != 1 || s.Communicated[0].State != Communicated || s.Communicated[0].LastAttempt == nil {
		t.Fatalf("communicated history = %+v", s.Communicated)
	}
	if err := c.FinishAttempt(lease, []string{refs[1].ResultID}, good); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease finish = %v", err)
	}
}

func TestFinishTextCommitAndHoldAttempt(t *testing.T) {
	c, scope, _, lease, refs := completionFixture(t, 1)
	text := DeliveryReceipt{Scope: scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: "text"}
	if err := c.FinishAttempt(lease, []string{refs[0].ResultID}, text); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncommitted text receipt = %v", err)
	}
	if err := c.HoldAttempt(lease, "tts_failed"); err != nil {
		t.Fatal(err)
	}
	p := c.Pending(scope)
	if len(p) != 1 || p[0].State != Held || p[0].HoldReason != "tts_failed" || p[0].AnnouncementState != AnnouncementHeld {
		t.Fatalf("held result = %+v", p)
	}
	if err := c.Resume(scope, refs[0].ResultID); err != nil {
		t.Fatal(err)
	}
	p = c.Pending(scope)
	if p[0].State != Ready || p[0].HoldReason != "" || p[0].AnnouncementState != AnnouncementPending {
		t.Fatalf("resumed result = %+v", p[0])
	}
}

func TestCommunicatedIDCannotBeReadmittedAndInterruptedTextStaysPending(t *testing.T) {
	c, scope, _, lease, refs := completionFixture(t, 1)
	interrupted := DeliveryReceipt{Scope: scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: "text", ResponseCommitted: true, Interrupted: true}
	if err := c.FinishAttempt(lease, []string{refs[0].ResultID}, interrupted); !errors.Is(err, ErrConflict) {
		t.Fatalf("interrupted text receipt = %v", err)
	}
	if got := c.Pending(scope); len(got) != 1 || got[0].AnnouncementState != AnnouncementScheduled {
		t.Fatalf("interrupted text consumed work: %+v", got)
	}
	if err := c.HoldAttempt(lease, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(scope); got[0].LastAttempt == nil || got[0].LastAttempt.Reason != "interrupted" {
		t.Fatalf("failed attempt evidence = %+v", got)
	}
}

func TestCommunicatedHistoryRestoresWithoutReannouncementAndIsBounded(t *testing.T) {
	scope := testScope()
	c := NewCoordinator(scope, WithPendingLimit(2))
	for i := 0; i < 5; i++ {
		k, err := c.Register(scope, Admission{Task: "task"})
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Publish(k, Outcome{Scope: scope, JobID: k.JobID, Status: OutcomeSucceeded, Text: "ok"})
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.AcquireCapturePermit(scope, "capture", uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		l, err := c.Claim(scope, "attempt", "turn", []ResultRef{{Scope: scope, JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision}}, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.FinishAttempt(l, []string{r.Outcome.ResultID}, DeliveryReceipt{Scope: scope, AttemptID: "attempt", TurnID: "turn", CaptureEpoch: uint64(i + 1), Mode: "text", ResponseCommitted: true}); err != nil {
			t.Fatal(err)
		}
	}
	s := c.Snapshot(scope)
	if len(s.Communicated) != 2 || len(s.Pending) != 0 {
		t.Fatalf("bounded history = %+v", s)
	}
	destination := WorkScope{"session-1", "runtime-delivery-restore", "conversation-delivery-restore"}
	d := NewCoordinator(scope, WithPendingLimit(2))
	if err := d.Restore(s, destination); err != nil {
		t.Fatal(err)
	}
	if got := d.Pending(destination); len(got) != 0 {
		t.Fatalf("restored communicated results reannounced = %+v", got)
	}
	if got := d.Snapshot(destination).Communicated; len(got) != 2 || got[0].DeliveryKey.Scope != destination {
		t.Fatalf("restored communicated history = %+v", got)
	}
}

func TestFinishCommunicatedIDCannotBeReadmitted(t *testing.T) {
	c, scope, _, lease, refs := completionFixture(t, 1)
	if err := c.FinishAttempt(lease, []string{refs[0].ResultID}, DeliveryReceipt{Scope: scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: "text", ResponseCommitted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(scope, Admission{JobID: refs[0].JobID}); err == nil {
		t.Fatal("communicated job id was readmitted")
	}
}
