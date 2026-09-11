package work

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func testScope() WorkScope { return WorkScope{"session-1", "runtime-1", "conversation-1"} }

func TestPublishIsIdempotentAndRejectsConflict(t *testing.T) {
	c := NewCoordinator(testScope())
	k, err := c.Register(testScope(), Admission{Task: "scan", Origin: "agency", Provider: "local", Model: "m", Config: map[string]string{"temperature": "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Started(k); err != nil {
		t.Fatal(err)
	}
	o := Outcome{Scope: testScope(), JobID: k.JobID, TerminalRevision: 7, Status: OutcomeSucceeded, Text: "done", StartedAt: time.Now().Add(-time.Second)}
	a, err := c.Publish(k, o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Publish(k, o)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome.ResultID != b.Outcome.ResultID || len(c.Pending(testScope())) != 1 {
		t.Fatalf("duplicate publish changed inbox: %#v %#v", a, b)
	}
	o.Text = "different"
	if _, err := c.Publish(k, o); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestLateResultAndResetAreFenced(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "late"})
	next := WorkScope{"session-1", "runtime-2", "conversation-2"}
	if err := c.Reset(next); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(k, Outcome{Scope: k.Scope, JobID: k.JobID, Status: OutcomeSucceeded, Text: "late"}); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("late publish = %v", err)
	}
	if got := c.Snapshot(next); len(got.Jobs) != 0 {
		t.Fatalf("reset retained old jobs: %+v", got)
	}
}

func TestRestoreHoldsPendingAndCopiesImmutableState(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "x", Config: map[string]string{"model": "a"}})
	o, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "answer"})
	s := c.Snapshot(testScope())
	s.Pending[0].Outcome.Text = "mutated"
	s.Jobs[0].Config["model"] = "mutated"
	if c.Pending(testScope())[0].Outcome.Text != o.Outcome.Text {
		t.Fatal("snapshot mutation changed coordinator")
	}
	other := NewCoordinator(testScope())
	next := WorkScope{"session-1", "runtime-2", "conversation-2"}
	if err := other.Restore(c.Snapshot(testScope()), next); err != nil {
		t.Fatal(err)
	}
	v := other.Pending(next)
	if len(v) != 1 || v[0].State != Held || !v[0].Outcome.PreviousSession || v[0].DeliveryKey.Scope != next {
		t.Fatalf("restored pending = %+v", v)
	}
}

func TestCallbackCanReenterCoordinator(t *testing.T) {
	var c *Coordinator
	done := make(chan struct{})
	c = NewCoordinator(testScope(), WithChangeCallback(func(s Snapshot) { _ = c.Snapshot(s.Scope); close(done) }))
	if _, err := c.Register(testScope(), Admission{Task: "reenter"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback did not run")
	}
}

func TestConcurrentPublishOneTerminalOutcome(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "race"})
	o := Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "same"}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Publish(k, o)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("duplicate publish failed: %v", err)
		}
	}
	if got := len(c.Pending(testScope())); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}
}

func TestPendingLimitIsExplicit(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(1))
	k1, _ := c.Register(testScope(), Admission{Task: "one"})
	if _, err := c.Register(testScope(), Admission{Task: "two"}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("admission overflow = %v", err)
	}
	_, _ = c.Publish(k1, Outcome{Scope: testScope(), JobID: k1.JobID, Status: OutcomeSucceeded, Text: "one"})
	if got := len(c.Pending(testScope())); got != 1 {
		t.Fatalf("retained completion = %d", got)
	}
}

func TestAdmissionProvenanceCannotBeRewritten(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Origin: "agency", Provider: "local", Model: "m", Config: map[string]string{"x": "1"}})
	_, err := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Origin: "other", Text: "x"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("provenance override = %v", err)
	}
	r, err := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome.Origin != "agency" || r.Outcome.Provider != "local" || r.Outcome.Config["x"] != "1" {
		t.Fatalf("frozen provenance = %+v", r.Outcome)
	}
}

func TestRestoreInterruptedRunningUsesFreshDeliveryScope(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "running"})
	_ = c.Started(k)
	dest := WorkScope{"session-1", "runtime-2", "conversation-2"}
	restored := NewCoordinator(testScope())
	if err := restored.Restore(c.Snapshot(testScope()), dest); err != nil {
		t.Fatal(err)
	}
	p := restored.Pending(dest)
	if len(p) != 1 || p[0].Outcome.Status != OutcomeInterrupted || p[0].DeliveryKey.Scope != dest || p[0].Outcome.Scope != testScope() {
		t.Fatalf("restored running = %+v", p)
	}
}

func TestResetFencesPersistedAcknowledgement(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "x"})
	_ = k
	old := testScope()
	next := WorkScope{"session-1", "runtime-2", "conversation-2"}
	if err := c.Reset(next); err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgePersisted(old, 1); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("old ack = %v", err)
	}
}

func TestResultIDCollisionAcrossJobsIsRejected(t *testing.T) {
	c := NewCoordinator(testScope())
	a, _ := c.Register(testScope(), Admission{Task: "a"})
	b, _ := c.Register(testScope(), Admission{Task: "b"})
	if _, err := c.Publish(a, Outcome{Scope: testScope(), JobID: a.JobID, ResultID: "shared", Status: OutcomeSucceeded, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(b, Outcome{Scope: testScope(), JobID: b.JobID, ResultID: "shared", Status: OutcomeSucceeded, Text: "b"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("collision = %v", err)
	}
	if got := c.Pending(testScope()); len(got) != 1 || got[0].Outcome.JobID != a.JobID {
		t.Fatalf("collision overwrote result: %+v", got)
	}
}

func TestDismissedCyclesStayBoundedAndRestoreCanAdmit(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(2))
	for i := 0; i < 20; i++ {
		k, err := c.Register(testScope(), Admission{Task: "cycle"})
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "ok"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Dismiss(testScope(), r.Outcome.ResultID); err != nil {
			t.Fatal(err)
		}
	}
	s := c.Snapshot(testScope())
	if len(s.Jobs) > 2 || len(s.Pending) > 2 {
		t.Fatalf("unbounded snapshot jobs=%d pending=%d", len(s.Jobs), len(s.Pending))
	}
	next := WorkScope{"session-1", "runtime-2", "conversation-2"}
	restored := NewCoordinator(testScope(), WithPendingLimit(2))
	if err := restored.Restore(s, next); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Register(next, Admission{Task: "after restore"}); err != nil {
		t.Fatalf("restore did not free capacity: %v", err)
	}
}

func TestMalformedRestoreLeavesStateUnchanged(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "keep"})
	before := c.Snapshot(testScope())
	bad := before
	bad.Jobs = []JobView{{Key: JobKey{Scope: testScope(), JobID: k.JobID}, State: JobTerminal}}
	dest := WorkScope{"session-1", "runtime-2", "conversation-2"}
	if err := c.Restore(bad, dest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad restore = %v", err)
	}
	after := c.Snapshot(testScope())
	if len(after.Jobs) != len(before.Jobs) || after.Scope != before.Scope {
		t.Fatalf("failed restore mutated state: before=%+v after=%+v", before, after)
	}
}

func TestGeneratedIDSkipsRetiredTombstone(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(2))
	k, _ := c.Register(testScope(), Admission{JobID: "work-1"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "x"})
	_ = c.Dismiss(testScope(), r.Outcome.ResultID)
	got, err := c.Register(testScope(), Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID == "work-1" {
		t.Fatalf("generated id reused tombstone: %q", got.JobID)
	}
}

func TestSuccessiveRestorePreservesProvenanceAndPairs(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "x"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "answer", SourceRef: "src"})
	first := WorkScope{"session-1", "runtime-2", "conversation-2"}
	second := WorkScope{"session-1", "runtime-3", "conversation-3"}
	restored := NewCoordinator(testScope())
	if err := restored.Restore(c.Snapshot(testScope()), first); err != nil {
		t.Fatal(err)
	}
	p := restored.Pending(first)
	if err := restored.Hold(first, p[0].Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(restored.Snapshot(first), second); err != nil {
		t.Fatal(err)
	}
	p = restored.Pending(second)
	if len(p) != 1 || p[0].DeliveryKey.Scope != second || p[0].Outcome.Scope != testScope() || p[0].Outcome.SourceRef != r.Outcome.SourceRef {
		t.Fatalf("successive restore = %+v", p)
	}
	if err := restored.Resume(second, p[0].Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
	if err := restored.Dismiss(second, p[0].Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsPairedOutcomeMutation(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "x"})
	_, _ = c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "answer"})
	s := c.Snapshot(testScope())
	s.Pending[0].Outcome.Text = "tampered"
	dest := WorkScope{"session-1", "runtime-2", "conversation-2"}
	if err := c.Restore(s, dest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("paired mutation accepted: %v", err)
	}
}

func TestResetClearsScopeOwnedCapacityAndIDs(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(2))
	oldKey, _ := c.Register(testScope(), Admission{JobID: "old-id"})
	_, _ = c.Register(testScope(), Admission{Task: "full"})
	if _, err := c.Register(testScope(), Admission{Task: "overflow"}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("pre-reset admission = %v", err)
	}
	next := WorkScope{"session-1", "runtime-reset", "conversation-reset"}
	if err := c.Reset(next); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(next, Admission{JobID: "old-id"}); err != nil {
		t.Fatalf("old explicit id blocked after reset: %v", err)
	}
	if _, err := c.Register(next, Admission{Task: "full-2"}); err != nil {
		t.Fatalf("first fresh admission: %v", err)
	}
	if _, err := c.Register(next, Admission{Task: "overflow-2"}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("fresh capacity = %v", err)
	}
	if _, err := c.Publish(oldKey, Outcome{Scope: oldKey.Scope, JobID: oldKey.JobID, Status: OutcomeSucceeded, Text: "late"}); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("old callback accepted: %v", err)
	}
}

func TestCurrentHoldDoesNotMarkPreviousSession(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "current"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "done"})
	if err := c.Hold(testScope(), r.Outcome.ResultID); err != nil {
		t.Fatal(err)
	}
	p := c.Pending(testScope())
	if len(p) != 1 || p[0].Outcome.PreviousSession {
		t.Fatalf("current hold marked historic: %+v", p)
	}
}

func TestRetireFinalizesLiveJobsAndRestoresHeld(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(4))
	queued, _ := c.Register(testScope(), Admission{JobID: "queued", Origin: "agency", Provider: "local", Model: "m", Config: map[string]string{"x": "1"}})
	running, _ := c.Register(testScope(), Admission{JobID: "running", Origin: "managed", Provider: "obey", Model: "m2"})
	if err := c.Started(running); err != nil {
		t.Fatal(err)
	}
	if err := c.Retire(testScope()); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot(testScope())
	if !s.Retired || len(s.Pending) != 2 {
		t.Fatalf("retired snapshot = %+v", s)
	}
	for _, j := range s.Jobs {
		if j.State != JobTerminal || j.Outcome == nil || j.Outcome.Status != OutcomeInterrupted || j.Outcome.Elapsed != 0 || !j.Outcome.EndedAt.IsZero() {
			t.Fatalf("retired job=%+v", j)
		}
		if j.Key.JobID == queued.JobID && j.Outcome.Origin != "agency" {
			t.Fatalf("queued provenance=%+v", j.Outcome)
		}
	}
	for _, p := range s.Pending {
		if p.State != Pending || p.Outcome.Text != "Conversation ended before work completion was confirmed." || p.Outcome.Termination != "unknown" {
			t.Fatalf("retired pending=%+v", p)
		}
	}
	next := WorkScope{"session-1", "runtime-retired", "conversation-retired"}
	restored := NewCoordinator(testScope(), WithPendingLimit(4))
	if err := restored.Restore(s, next); err != nil {
		t.Fatal(err)
	}
	held := restored.Pending(next)
	if len(held) != 2 {
		t.Fatalf("restored held=%+v", held)
	}
	for _, p := range held {
		if p.State != Held || !p.Outcome.PreviousSession {
			t.Fatalf("restored retired result=%+v", p)
		}
	}
}

func TestRetirePublishRaceLeavesOneImmutableTerminalOutcome(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{JobID: "race"})
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() { defer wg.Done(); errs <- c.Retire(testScope()) }()
	go func() {
		defer wg.Done()
		_, err := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "late"})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	s := c.Snapshot(testScope())
	if len(s.Pending) != 1 || len(s.Jobs) != 1 || s.Jobs[0].Outcome == nil {
		t.Fatalf("race snapshot=%+v", s)
	}
	if s.Jobs[0].Outcome.Status != OutcomeSucceeded && s.Jobs[0].Outcome.Status != OutcomeInterrupted {
		t.Fatalf("race outcome=%+v", s.Jobs[0].Outcome)
	}
}

func TestRetireDoesNotOverwriteCustomResultID(t *testing.T) {
	c := NewCoordinator(testScope(), WithPendingLimit(3))
	completed, _ := c.Register(testScope(), Admission{JobID: "completed"})
	if _, err := c.Publish(completed, Outcome{Scope: testScope(), JobID: completed.JobID, ResultID: "live-result-1", Status: OutcomeSucceeded, Text: "keep"}); err != nil {
		t.Fatal(err)
	}
	live, _ := c.Register(testScope(), Admission{JobID: "live"})
	if err := c.Retire(testScope()); err != nil {
		t.Fatal(err)
	}
	pending := c.Pending(testScope())
	if len(pending) != 2 {
		t.Fatalf("pending=%+v", pending)
	}
	seen := map[string]Outcome{}
	for _, p := range pending {
		seen[p.Outcome.ResultID] = p.Outcome
	}
	if seen["live-result-1"].Text != "keep" {
		t.Fatalf("custom result overwritten: %+v", seen)
	}
	for id, o := range seen {
		if o.JobID == live.JobID && id == "live-result-1" {
			t.Fatalf("retirement reused custom result id: %+v", seen)
		}
	}
}

func TestRestoreInterruptedIDDoesNotOverwriteCustomResultID(t *testing.T) {
	source := NewCoordinator(testScope(), WithPendingLimit(3))
	completed, _ := source.Register(testScope(), Admission{JobID: "completed"})
	if _, err := source.Publish(completed, Outcome{Scope: testScope(), JobID: completed.JobID, ResultID: "live-interrupted", Status: OutcomeSucceeded, Text: "keep"}); err != nil {
		t.Fatal(err)
	}
	_, _ = source.Register(testScope(), Admission{JobID: "live"})
	destination := WorkScope{"session-1", "runtime-restore-collision", "conversation-restore-collision"}
	restored := NewCoordinator(testScope(), WithPendingLimit(3))
	if err := restored.Restore(source.Snapshot(testScope()), destination); err != nil {
		t.Fatal(err)
	}
	got := restored.Pending(destination)
	if len(got) != 2 {
		t.Fatalf("restored pending=%+v", got)
	}
	seen := map[string]Outcome{}
	for _, p := range got {
		seen[p.Outcome.ResultID] = p.Outcome
	}
	if seen["live-interrupted"].Text != "keep" {
		t.Fatalf("custom restore result overwritten: %+v", seen)
	}
	for id, o := range seen {
		if o.JobID == "live" && id == "live-interrupted" {
			t.Fatalf("restore reused custom result id: %+v", seen)
		}
	}
}

func TestCompletionLeaseHasOneOwnerAndDuplicateClaimLoses(t *testing.T) {
	c := NewCoordinator(testScope())
	k, err := c.Register(testScope(), Admission{Task: "completion"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.AcquireCapturePermit(testScope(), "capture-a", 9)
	if err != nil {
		t.Fatal(err)
	}
	ref := ResultRef{Scope: testScope(), JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision}
	lease, err := c.Claim(testScope(), "attempt-a", "turn-a", []ResultRef{ref}, p)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.Valid() || !p.Valid() || c.ValidateCompletionLease(lease) != nil {
		t.Fatal("claimed lease is not valid")
	}
	if _, err := c.Claim(testScope(), "attempt-b", "turn-b", []ResultRef{ref}, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate claim = %v", err)
	}
	if got := c.Pending(testScope()); len(got) != 1 || got[0].State != Pending {
		t.Fatalf("claim consumed result: %+v", got)
	}
	if err := c.RevokeCompletionLease(lease); err != nil {
		t.Fatal(err)
	}
	if lease.Valid() || p.Valid() {
		t.Fatal("revocation left fence valid")
	}
	if _, err := c.Claim(testScope(), "attempt-c", "turn-c", []ResultRef{ref}, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale permit claim = %v", err)
	}
}

func TestCapturePermitWriteIsFencedByRevoke(t *testing.T) {
	c := NewCoordinator(testScope())
	p, err := c.AcquireCapturePermit(testScope(), "capture", 1)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if !p.Write(func() { called = true }) || !called {
		t.Fatal("active permit rejected write")
	}
	if err := c.RevokeCapturePermit(p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("revoked permit did not close Done")
	}
	called = false
	if p.Write(func() { called = true }) || called {
		t.Fatal("revoked permit accepted write")
	}
}

func TestCompletionLeaseStaleAfterReset(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "completion"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "done"})
	p, _ := c.AcquireCapturePermit(testScope(), "capture", 1)
	lease, err := c.Claim(testScope(), "attempt", "turn", []ResultRef{{Scope: testScope(), JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision}}, p)
	if err != nil {
		t.Fatal(err)
	}
	next := WorkScope{"session-1", "runtime-next", "conversation-next"}
	if err := c.Reset(next); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateCompletionLease(lease); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("old lease validation = %v", err)
	}
	if lease.Valid() || p.Valid() {
		t.Fatal("reset left old fence valid")
	}
	if _, err := c.AcquireCapturePermit(next, "new-capture", 2); err != nil {
		t.Fatalf("new scope permit = %v", err)
	}
}

func TestCompletionLeaseMetadataMutationIsRejected(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "completion"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "done"})
	p, _ := c.AcquireCapturePermit(testScope(), "capture", 1)
	lease, err := c.Claim(testScope(), "attempt", "turn", []ResultRef{{Scope: testScope(), JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision}}, p)
	if err != nil {
		t.Fatal(err)
	}
	lease.AttemptID = "forged-attempt"
	lease.Results[0].ResultID = "forged-result"
	if lease.Valid() || lease.Write(func() {}) {
		t.Fatal("mutated lease remained usable")
	}
	if err := c.ValidateCompletionLease(lease); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutated lease validation = %v", err)
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("mutated lease exposed live Done channel")
	}
	if !p.Valid() {
		t.Fatal("lease metadata mutation revoked the permit")
	}
	p.PermitID = "forged-permit"
	if p.Valid() || p.Write(func() {}) {
		t.Fatal("mutated permit remained usable")
	}
}

func TestRevokeBindingPreservesResultsAndWaitsForWrite(t *testing.T) {
	c := NewCoordinator(testScope())
	k, _ := c.Register(testScope(), Admission{Task: "completion"})
	r, _ := c.Publish(k, Outcome{Scope: testScope(), JobID: k.JobID, Status: OutcomeSucceeded, Text: "done"})
	p, _ := c.AcquireCapturePermit(testScope(), "capture", 1)
	lease, err := c.Claim(testScope(), "attempt", "turn", []ResultRef{{Scope: testScope(), JobID: k.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision}}, p)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, revoked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		if !lease.Write(func() { close(entered); <-release }) {
			t.Error("active lease rejected write")
		}
	}()
	<-entered
	go func() {
		if err := c.RevokeBinding(testScope()); err != nil {
			t.Error(err)
		}
		close(revoked)
	}()
	select {
	case <-revoked:
		t.Fatal("binding revoked while write callback was still running")
	default:
	}
	close(release)
	<-revoked
	if lease.Valid() || p.Valid() {
		t.Fatal("binding revoke left lease or permit valid")
	}
	if got := c.Pending(testScope()); len(got) != 1 || got[0].Outcome.ResultID != r.Outcome.ResultID {
		t.Fatalf("binding revoke changed results: %+v", got)
	}
	if _, err := c.AcquireCapturePermit(testScope(), "replacement", 2); err != nil {
		t.Fatalf("replacement permit = %v", err)
	}
}
