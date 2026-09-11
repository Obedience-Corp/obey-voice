package work

import (
	"errors"
	"testing"
)

func TestPendingResultsFollowArrivalRatherThanLexicalJobIDs(t *testing.T) {
	scope := testScope()
	c := NewCoordinator(scope)
	for _, id := range []string{"z-first", "a-last"} {
		key, err := c.Register(scope, Admission{JobID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Publish(key, Outcome{Scope: scope, JobID: id, Status: OutcomeSucceeded, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	pending := c.Pending(scope)
	if len(pending) != 2 || pending[0].Outcome.JobID != "z-first" {
		t.Fatalf("arrival order=%+v", pending)
	}
	destination := scope
	destination.RuntimeEpoch = "restored-runtime"
	restored := NewCoordinator(scope)
	if err := restored.Restore(c.Snapshot(scope), destination); err != nil {
		t.Fatal(err)
	}
	if got := restored.Pending(destination); len(got) != 2 || got[0].Outcome.JobID != "z-first" || got[0].ReceivedRevision != pending[0].ReceivedRevision {
		t.Fatalf("restored arrival order=%+v", got)
	}
}

func TestRestoreInterruptedWorkerRetainsAdmissionProvenance(t *testing.T) {
	scope := testScope()
	c := NewCoordinator(scope)
	key, err := c.Register(scope, Admission{Task: "weather", Origin: "agency", Provider: "ollama", Model: "old-model", Config: map[string]string{"persona": "original"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Started(key); err != nil {
		t.Fatal(err)
	}
	destination := scope
	destination.RuntimeEpoch = "after-crash"
	restored := NewCoordinator(scope)
	if err := restored.Restore(c.Snapshot(scope), destination); err != nil {
		t.Fatal(err)
	}
	r := restored.Pending(destination)[0]
	if r.Task != "weather" || r.Outcome.Origin != "agency" || r.Outcome.Provider != "ollama" || r.Outcome.Model != "old-model" || r.Outcome.Config["persona"] != "original" || r.Outcome.TerminalRevision != 1 || r.State != Held {
		t.Fatalf("interrupted admission provenance=%+v", r)
	}
}

func TestHoldingOneClaimedResultReleasesPeersAndRecordsRevocation(t *testing.T) {
	c, scope, _, lease, refs := completionFixture(t, 2)
	if err := c.HoldWithReason(scope, refs[0].ResultID, "audio_muted"); err != nil {
		t.Fatal(err)
	}
	if lease.Valid() {
		t.Fatal("held batch kept playback authority")
	}
	for _, r := range c.Pending(scope) {
		if r.LastAttempt == nil || r.LastAttempt.AttemptID != lease.AttemptID {
			t.Fatalf("missing original attempt: %+v", r)
		}
		if r.Outcome.ResultID == refs[0].ResultID {
			if r.State != Held || r.AnnouncementState != AnnouncementHeld {
				t.Fatalf("hold lost: %+v", r)
			}
		} else if r.AnnouncementState != AnnouncementPending {
			t.Fatalf("unowned peer still scheduled: %+v", r)
		}
	}
}

func TestBindingRevocationPersistsOriginalAttempt(t *testing.T) {
	c, scope, _, lease, _ := completionFixture(t, 1)
	if err := c.RevokeBinding(scope); err != nil {
		t.Fatal(err)
	}
	r := c.Snapshot(scope).Pending[0]
	if r.LastAttempt == nil || r.LastAttempt.AttemptID != lease.AttemptID || r.LastAttempt.Reason != "lease_revoked" || r.LastAttempt.Addressed {
		t.Fatalf("revocation evidence: %+v", r.LastAttempt)
	}
}

func TestRestoreRejectsCommunicatedRecordsWithoutValidReceipt(t *testing.T) {
	c, scope, _, l, refs := completionFixture(t, 1)
	if err := c.FinishAttempt(l, []string{refs[0].ResultID}, DeliveryReceipt{Scope: scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, Mode: "text", ResponseCommitted: true}); err != nil {
		t.Fatal(err)
	}
	for name, damage := range map[string]func(*ResultView){
		"missing":       func(r *ResultView) { r.LastAttempt = nil },
		"uncommitted":   func(r *ResultView) { r.LastAttempt.Receipt.ResponseCommitted = false },
		"wrong_attempt": func(r *ResultView) { r.LastAttempt.Receipt.AttemptID = "unrelated" },
		"unaddressed":   func(r *ResultView) { r.LastAttempt.Addressed = false },
	} {
		t.Run(name, func(t *testing.T) {
			s := c.Snapshot(scope)
			damage(&s.Communicated[0])
			dest := scope
			dest.ConversationEpoch = "new"
			restored := NewCoordinator(scope)
			if err := restored.Restore(s, dest); !errors.Is(err, ErrInvalid) {
				t.Fatalf("malformed receipt restored: %v", err)
			}
			if restored.Scope() != scope {
				t.Fatal("invalid restore changed scope")
			}
		})
	}
}

func TestCommunicatedResultCannotCollideWithLaterPublication(t *testing.T) {
	c, scope, _, l, refs := completionFixture(t, 1)
	if err := c.FinishAttempt(l, []string{refs[0].ResultID}, DeliveryReceipt{Scope: scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, Mode: "text", ResponseCommitted: true}); err != nil {
		t.Fatal(err)
	}
	k, err := c.Register(scope, Admission{Task: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(k, Outcome{Scope: scope, JobID: k.JobID, ResultID: refs[0].ResultID, Status: OutcomeSucceeded, Text: "unrelated"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused result ID: %v", err)
	}
}
