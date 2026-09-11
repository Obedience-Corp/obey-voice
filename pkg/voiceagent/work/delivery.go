package work

import "fmt"

func (c *Coordinator) validActiveLeaseLocked(l CompletionLease) error {
	if err := c.checkScopeLocked(l.Scope); err != nil {
		return err
	}
	if c.activeLease != l.lease || !leaseIdentityMatchesLocked(l) || !c.permitMatchesLocked(CapturePermit{Scope: l.Scope, PermitID: l.PermitID, CaptureEpoch: l.CaptureEpoch, state: l.permit}) {
		return ErrConflict
	}
	return nil
}

func deliveryReceiptMatchesLease(l CompletionLease, r DeliveryReceipt) bool {
	return r.Scope == l.Scope && r.AttemptID == l.AttemptID && r.TurnID == l.TurnID && r.CaptureEpoch == l.CaptureEpoch
}

func deliverySuccessful(r DeliveryReceipt) bool {
	switch r.Mode {
	case "text":
		return r.ResponseCommitted && !r.Interrupted
	case "voice":
		return r.ResponseCommitted && r.PlaybackComplete && r.Audible && r.Segments > 0 && !r.Interrupted
	default:
		return false
	}
}

// FinishAttempt commits only the addressed subset after an exact successful
// receipt. Validation is complete before any result is changed.
func (c *Coordinator) FinishAttempt(l CompletionLease, addressedIDs []string, receipt DeliveryReceipt) error {
	c.mu.Lock()
	if err := c.validActiveLeaseLocked(l); err != nil {
		c.mu.Unlock()
		return err
	}
	if !deliveryReceiptMatchesLease(l, receipt) || !deliverySuccessful(receipt) {
		c.mu.Unlock()
		return fmt.Errorf("%w: incomplete delivery receipt", ErrConflict)
	}
	refs := make(map[string]ResultRef, len(l.Results))
	for _, ref := range l.Results {
		refs[ref.ResultID] = ref
	}
	seen := make(map[string]struct{}, len(addressedIDs))
	for _, id := range addressedIDs {
		if id == "" {
			c.mu.Unlock()
			return fmt.Errorf("%w: empty addressed result", ErrInvalid)
		}
		if _, ok := seen[id]; ok {
			c.mu.Unlock()
			return fmt.Errorf("%w: duplicate addressed result", ErrConflict)
		}
		seen[id] = struct{}{}
		if _, ok := refs[id]; !ok {
			c.mu.Unlock()
			return fmt.Errorf("%w: addressed result was not claimed", ErrConflict)
		}
	}
	if len(seen) == 0 {
		c.mu.Unlock()
		return fmt.Errorf("%w: no addressed results", ErrInvalid)
	}
	for _, ref := range l.Results {
		r := c.results[ref.ResultID]
		if r == nil || r.view.Outcome.TerminalRevision != ref.Revision {
			c.mu.Unlock()
			return ErrConflict
		}
	}
	for _, ref := range l.Results {
		r := c.results[ref.ResultID]
		if _, addressed := seen[ref.ResultID]; addressed {
			r.view.State = Communicated
			r.view.AnnouncementState = AnnouncementCommunicated
			r.view.HoldReason = ""
			r.view.LastAttempt = &DeliveryEvidence{Scope: l.Scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, Mode: receipt.Mode, ResultID: ref.ResultID, Addressed: true, Receipt: receipt}
			c.communicated[ref.ResultID] = cloneResult(r.view)
			c.communicatedOrder = append(c.communicatedOrder, ref.ResultID)
			c.tombstones[ref.JobID] = struct{}{}
			c.tombstoneOrder = append(c.tombstoneOrder, ref.JobID)
			delete(c.results, ref.ResultID)
			delete(c.jobs, ref.JobID)
			if c.reserved > 0 {
				c.reserved--
			}
		} else {
			r.view.AnnouncementState = AnnouncementPending
			r.view.LastAttempt = &DeliveryEvidence{Scope: l.Scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, ResultID: ref.ResultID, Reason: "coverage_omitted"}
		}
	}
	c.trimCommunicatedLocked()
	c.trimTombstonesLocked()
	c.closeLeaseLocked(fenceReleased)
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

// HoldAttempt records a failed or interrupted delivery as held and releases
// the lease. The caller can later Resume it after an explicit capability or
// user action becomes available.
func (c *Coordinator) HoldAttempt(l CompletionLease, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: empty hold reason", ErrInvalid)
	}
	c.mu.Lock()
	if err := c.validActiveLeaseLocked(l); err != nil {
		c.mu.Unlock()
		return err
	}
	for _, ref := range l.Results {
		r := c.results[ref.ResultID]
		if r == nil || r.view.Outcome.TerminalRevision != ref.Revision {
			c.mu.Unlock()
			return ErrConflict
		}
	}
	for _, ref := range l.Results {
		r := c.results[ref.ResultID]
		r.view.State = Held
		r.view.AnnouncementState = AnnouncementHeld
		r.view.HoldReason = reason
		r.view.LastAttempt = &DeliveryEvidence{Scope: l.Scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, ResultID: ref.ResultID, Addressed: false, Reason: reason}
	}
	c.closeLeaseLocked(fenceReleased)
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

// HoldWithReason holds a result without claiming an active completion lease.
func (c *Coordinator) HoldWithReason(scope WorkScope, resultID, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: empty hold reason", ErrInvalid)
	}
	c.mu.Lock()
	if err := c.checkScopeLocked(scope); err != nil {
		c.mu.Unlock()
		return err
	}
	r, ok := c.results[resultID]
	if !ok || r.view.DeliveryKey.Scope != scope {
		c.mu.Unlock()
		return ErrUnknown
	}
	r.view.State, r.view.AnnouncementState, r.view.HoldReason = Held, AnnouncementHeld, reason
	if c.activeLease != nil && leaseContainsResult(c.activeLease, resultID) {
		c.recordAbandonedLeaseLocked("disposition_changed")
		r.view.LastAttempt.Reason = reason
		c.closeLeaseLocked(fenceRevoked)
	}
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

func (c *Coordinator) trimCommunicatedLocked() {
	for len(c.communicatedOrder) > c.pendingLimit {
		id := c.communicatedOrder[0]
		c.communicatedOrder = c.communicatedOrder[1:]
		delete(c.communicated, id)
	}
}

func (c *Coordinator) communicatedJobLocked(id string) bool {
	for _, v := range c.communicated {
		if v.Outcome.JobID == id {
			return true
		}
	}
	return false
}

func (c *Coordinator) recordAbandonedLeaseLocked(reason string) {
	l := c.activeLease
	if l == nil {
		return
	}
	for _, ref := range l.refs {
		if r := c.results[ref.ResultID]; r != nil {
			r.view.AnnouncementState = AnnouncementPending
			if r.view.State == Held {
				r.view.AnnouncementState = AnnouncementHeld
			}
			r.view.LastAttempt = &DeliveryEvidence{Scope: l.scope, AttemptID: l.attemptID, TurnID: l.turnID, CaptureEpoch: l.captureEpoch, ResultID: ref.ResultID, Reason: reason}
		}
	}
}

func validCommunicatedEvidence(v ResultView, sessionID string) bool {
	e := v.LastAttempt
	if e == nil || !e.Addressed || e.ResultID != v.Outcome.ResultID || !e.Scope.Valid() || e.Scope.SessionID != sessionID || e.AttemptID == "" || e.TurnID == "" || e.CaptureEpoch == 0 {
		return false
	}
	r := e.Receipt
	return e.Mode == r.Mode && e.Scope == r.Scope && e.AttemptID == r.AttemptID && e.TurnID == r.TurnID && e.CaptureEpoch == r.CaptureEpoch && deliverySuccessful(r)
}
