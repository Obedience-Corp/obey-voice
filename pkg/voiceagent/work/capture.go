package work

import (
	"fmt"
	"sync"
)

// CapturePermit is an in-memory authority for one capture owner. It is
// deliberately absent from Snapshot: permits contain runtime synchronization
// state and must be reacquired after a scope change.
type CapturePermit struct {
	Scope        WorkScope `json:"scope"`
	PermitID     string    `json:"permit_id"`
	CaptureEpoch uint64    `json:"capture_epoch"`
	state        *permitState
}

// ResultRef identifies the immutable terminal observation a lease may address.
type ResultRef struct {
	Scope    WorkScope `json:"scope"`
	JobID    string    `json:"job_id"`
	ResultID string    `json:"result_id"`
	Revision uint64    `json:"revision"`
}

// CompletionLease is the single authority for one completion attempt. Its
// fence methods are intentionally compatible with playback/capture adapters,
// while the package remains independent of those adapters.
type CompletionLease struct {
	Scope        WorkScope   `json:"scope"`
	AttemptID    string      `json:"attempt_id"`
	TurnID       string      `json:"turn_id"`
	CaptureEpoch uint64      `json:"capture_epoch"`
	Results      []ResultRef `json:"results"`
	PermitID     string      `json:"permit_id"`
	permit       *permitState
	lease        *leaseState
}

type fenceState uint8

const (
	fenceActive fenceState = iota + 1
	fenceClaimed
	fenceReleased
	fenceRevoked
)

type permitState struct {
	mu           sync.Mutex
	state        fenceState
	done         chan struct{}
	scope        WorkScope
	id           string
	captureEpoch uint64
}

type leaseState struct {
	id           string
	permit       *permitState
	scope        WorkScope
	attemptID    string
	turnID       string
	captureEpoch uint64
	permitID     string
	refs         []ResultRef
}

// Write serializes a short adapter write with revocation. The callback must
// not call back into Coordinator; this method never acquires coordinator.mu.
func (p CapturePermit) Write(fn func()) bool {
	return writePermitFence(p, fn)
}

func (p CapturePermit) Valid() bool { return validPermit(p) }

func (p CapturePermit) Done() <-chan struct{} {
	if p.state == nil {
		return closedDone
	}
	p.state.mu.Lock()
	valid := permitIdentityMatchesLocked(p)
	done := p.state.done
	p.state.mu.Unlock()
	if !valid {
		return closedDone
	}
	return done
}

func (l CompletionLease) Write(fn func()) bool { return writeLeaseFence(l, fn) }
func (l CompletionLease) Valid() bool          { return validLease(l) }
func (l CompletionLease) Done() <-chan struct{} {
	if l.permit == nil {
		return closedDone
	}
	l.permit.mu.Lock()
	valid := leaseIdentityMatchesLocked(l)
	done := l.permit.done
	l.permit.mu.Unlock()
	if !valid {
		return closedDone
	}
	return done
}

var closedDone = func() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func writePermitFence(p CapturePermit, fn func()) bool {
	if p.state == nil || fn == nil {
		return false
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if !permitIdentityMatchesLocked(p) || (p.state.state != fenceActive && p.state.state != fenceClaimed) {
		return false
	}
	fn()
	return true
}

func writeLeaseFence(l CompletionLease, fn func()) bool {
	if l.permit == nil || l.lease == nil || fn == nil {
		return false
	}
	l.permit.mu.Lock()
	defer l.permit.mu.Unlock()
	if !leaseIdentityMatchesLocked(l) || (l.permit.state != fenceActive && l.permit.state != fenceClaimed) {
		return false
	}
	fn()
	return true
}

func validPermit(p CapturePermit) bool {
	if p.state == nil {
		return false
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	return permitIdentityMatchesLocked(p) && (p.state.state == fenceActive || p.state.state == fenceClaimed)
}

func validLease(l CompletionLease) bool {
	if l.permit == nil || l.lease == nil {
		return false
	}
	l.permit.mu.Lock()
	defer l.permit.mu.Unlock()
	return leaseIdentityMatchesLocked(l) && (l.permit.state == fenceActive || l.permit.state == fenceClaimed)
}

func permitIdentityMatchesLocked(p CapturePermit) bool {
	return p.state != nil && p.state.scope == p.Scope && p.state.id == p.PermitID && p.state.captureEpoch == p.CaptureEpoch
}

func leaseIdentityMatchesLocked(l CompletionLease) bool {
	if l.lease == nil || l.permit == nil || l.lease.permit != l.permit || l.Scope != l.lease.scope || l.AttemptID != l.lease.attemptID || l.TurnID != l.lease.turnID || l.CaptureEpoch != l.lease.captureEpoch || l.PermitID != l.lease.permitID || !sameResultRefs(l.Results, l.lease.refs) {
		return false
	}
	return permitIdentityMatchesLocked(CapturePermit{Scope: l.Scope, PermitID: l.PermitID, CaptureEpoch: l.CaptureEpoch, state: l.permit})
}

func sameResultRefs(a, b []ResultRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validFence(p *permitState) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == fenceActive || p.state == fenceClaimed
}

func closeFenceLocked(p *permitState, state fenceState) {
	if p == nil {
		return
	}
	if p.state == fenceReleased || p.state == fenceRevoked {
		return
	}
	p.state = state
	close(p.done)
}

// AcquireCapturePermit establishes the sole capture owner for the current
// scope. ownerID is diagnostic identity only; it is never persisted.
func (c *Coordinator) AcquireCapturePermit(scope WorkScope, ownerID string, captureEpoch uint64) (CapturePermit, error) {
	if ownerID == "" {
		return CapturePermit{}, fmt.Errorf("%w: empty capture owner", ErrInvalid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(scope); err != nil {
		return CapturePermit{}, err
	}
	if c.permit != nil {
		return CapturePermit{}, ErrConflict
	}
	c.nextPermit++
	permitID := fmt.Sprintf("capture-%d", c.nextPermit)
	p := &permitState{state: fenceActive, done: make(chan struct{}), scope: scope, id: permitID, captureEpoch: captureEpoch}
	permit := CapturePermit{Scope: scope, PermitID: permitID, CaptureEpoch: captureEpoch, state: p}
	c.permit = &activePermit{id: permit.PermitID, scope: scope, owner: ownerID, epoch: captureEpoch, state: p}
	return permit, nil
}

type activePermit struct {
	id, owner string
	scope     WorkScope
	epoch     uint64
	state     *permitState
}

func (c *Coordinator) permitMatchesLocked(p CapturePermit) bool {
	return c.permit != nil && p.state != nil && c.permit.state == p.state && c.permit.id == p.PermitID && c.permit.scope == p.Scope && c.permit.epoch == p.CaptureEpoch
}

func (c *Coordinator) ValidateCapturePermit(p CapturePermit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(p.Scope); err != nil {
		return err
	}
	if !c.permitMatchesLocked(p) || !validFence(p.state) {
		return ErrConflict
	}
	return nil
}

func (c *Coordinator) ReleaseCapturePermit(p CapturePermit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(p.Scope); err != nil {
		return err
	}
	if !c.permitMatchesLocked(p) {
		return ErrConflict
	}
	if c.activeLease != nil {
		return ErrConflict
	}
	p.state.mu.Lock()
	closeFenceLocked(p.state, fenceReleased)
	p.state.mu.Unlock()
	c.permit = nil
	return nil
}

func (c *Coordinator) RevokeCapturePermit(p CapturePermit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.permitMatchesLocked(p) {
		if p.Scope != c.scope {
			return ErrStaleScope
		}
		return ErrConflict
	}
	if c.activeLease != nil {
		c.activeLease = nil
	}
	p.state.mu.Lock()
	closeFenceLocked(p.state, fenceRevoked)
	p.state.mu.Unlock()
	c.permit = nil
	return nil
}

// RevokeBinding invalidates the capture/completion binding for a current
// scope while preserving every job and result disposition. It is used when a
// runtime/provider binding is replaced.
func (c *Coordinator) RevokeBinding(scope WorkScope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(scope); err != nil {
		return err
	}
	c.invalidateCaptureLocked()
	return nil
}

// Claim validates refs against the current inbox and reserves one completion
// attempt. It leaves result state unchanged; FinishAttempt is a later task.
func (c *Coordinator) Claim(scope WorkScope, attemptID, turnID string, refs []ResultRef, p CapturePermit) (CompletionLease, error) {
	if attemptID == "" || turnID == "" || len(refs) == 0 {
		return CompletionLease{}, fmt.Errorf("%w: incomplete completion claim", ErrInvalid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(scope); err != nil {
		return CompletionLease{}, err
	}
	if c.activeLease != nil || !c.permitMatchesLocked(p) {
		return CompletionLease{}, ErrConflict
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Scope != scope || ref.JobID == "" || ref.ResultID == "" {
			return CompletionLease{}, ErrStaleScope
		}
		if _, duplicate := seen[ref.ResultID]; duplicate {
			return CompletionLease{}, ErrConflict
		}
		seen[ref.ResultID] = struct{}{}
		r, ok := c.results[ref.ResultID]
		if !ok || r.view.Outcome.JobID != ref.JobID || r.view.DeliveryKey != (JobKey{Scope: scope, JobID: ref.JobID}) || (r.view.State != Pending && r.view.State != Ready) {
			return CompletionLease{}, ErrConflict
		}
		if ref.Revision == 0 || ref.Revision != r.view.Outcome.TerminalRevision {
			return CompletionLease{}, ErrConflict
		}
	}
	p.state.mu.Lock()
	if p.state.state != fenceActive {
		p.state.mu.Unlock()
		return CompletionLease{}, ErrConflict
	}
	p.state.state = fenceClaimed
	p.state.mu.Unlock()
	c.nextLease++
	lease := CompletionLease{Scope: scope, AttemptID: attemptID, TurnID: turnID, CaptureEpoch: p.CaptureEpoch, PermitID: p.PermitID, Results: append([]ResultRef(nil), refs...), permit: p.state}
	c.activeLease = &leaseState{id: fmt.Sprintf("lease-%d", c.nextLease), permit: p.state, scope: scope, attemptID: attemptID, turnID: turnID, captureEpoch: p.CaptureEpoch, permitID: p.PermitID, refs: append([]ResultRef(nil), refs...)}
	lease.lease = c.activeLease
	return lease, nil
}

func (c *Coordinator) ValidateCompletionLease(l CompletionLease) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(l.Scope); err != nil {
		return err
	}
	if c.activeLease != l.lease || !leaseIdentityMatchesLocked(l) || !validFence(l.permit) {
		return ErrConflict
	}
	return nil
}

func (c *Coordinator) ReleaseCompletionLease(l CompletionLease) error {
	return c.finishLease(l, fenceReleased)
}

func (c *Coordinator) RevokeCompletionLease(l CompletionLease) error {
	return c.finishLease(l, fenceRevoked)
}

func (c *Coordinator) finishLease(l CompletionLease, state fenceState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkScopeLocked(l.Scope); err != nil {
		return err
	}
	if c.activeLease != l.lease || !leaseIdentityMatchesLocked(l) || !c.permitMatchesLocked(CapturePermit{Scope: l.Scope, PermitID: l.PermitID, CaptureEpoch: l.CaptureEpoch, state: l.permit}) {
		return ErrConflict
	}
	l.permit.mu.Lock()
	closeFenceLocked(l.permit, state)
	l.permit.mu.Unlock()
	c.activeLease = nil
	c.permit = nil
	return nil
}

func (c *Coordinator) invalidateCaptureLocked() {
	if c.permit == nil {
		c.activeLease = nil
		return
	}
	c.permit.state.mu.Lock()
	closeFenceLocked(c.permit.state, fenceRevoked)
	c.permit.state.mu.Unlock()
	c.permit = nil
	c.activeLease = nil
}
