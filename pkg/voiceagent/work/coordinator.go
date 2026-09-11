// Package work owns the semantic state of background work. It deliberately
// does not run workers, invoke providers, or perform persistence itself.
package work

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrStaleScope = errors.New("work scope is stale")
	ErrConflict   = errors.New("conflicting terminal outcome")
	ErrOverflow   = errors.New("pending work inbox is full")
	ErrRetired    = errors.New("work scope is retired")
	ErrUnknown    = errors.New("unknown work item")
	ErrInvalid    = errors.New("invalid work snapshot")
)

type WorkScope struct {
	SessionID         string `json:"session_id"`
	RuntimeEpoch      string `json:"runtime_id"`
	ConversationEpoch string `json:"conversation_epoch"`
}

func (s WorkScope) Valid() bool {
	return s.SessionID != "" && s.RuntimeEpoch != "" && s.ConversationEpoch != ""
}

type JobKey struct {
	Scope WorkScope `json:"scope"`
	JobID string    `json:"job_id"`
}

type Admission struct {
	JobID    string
	Task     string
	Origin   string
	Provider string
	Model    string
	// Config is copied at admission and never changes for this job.
	Config map[string]string
}

type JobState string

const (
	JobQueued   JobState = "queued"
	JobRunning  JobState = "running"
	JobTerminal JobState = "terminal"
)

type JobView struct {
	Key              JobKey            `json:"key"`
	Task             string            `json:"task"`
	Origin           string            `json:"origin"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	Config           map[string]string `json:"config"`
	State            JobState          `json:"state"`
	TerminalRevision uint64            `json:"terminal_revision"`
	Outcome          *Outcome          `json:"outcome"`
}

type OutcomeStatus string

const (
	OutcomeSucceeded   OutcomeStatus = "succeeded"
	OutcomePartial     OutcomeStatus = "partial"
	OutcomeFailed      OutcomeStatus = "failed"
	OutcomeCancelled   OutcomeStatus = "cancelled"
	OutcomeTimedOut    OutcomeStatus = "timed_out"
	OutcomeInterrupted OutcomeStatus = "interrupted"
)

// Outcome is immutable after Publish. Elapsed is frozen at publication.
type Outcome struct {
	ResultID         string            `json:"result_id"`
	Scope            WorkScope         `json:"scope"`
	JobID            string            `json:"job_id"`
	TerminalRevision uint64            `json:"terminal_revision"`
	Status           OutcomeStatus     `json:"status"`
	Text             string            `json:"text"`
	Origin           string            `json:"origin"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	Config           map[string]string `json:"config"`
	SourceRef        string            `json:"source_ref"`
	Limitations      string            `json:"limitations"`
	Termination      string            `json:"termination"`
	StartedAt        time.Time         `json:"started_at"`
	EndedAt          time.Time         `json:"ended_at"`
	Elapsed          time.Duration     `json:"elapsed_ns"`
	PreviousSession  bool              `json:"previous_session"`
}

type PendingState string

const (
	Pending      PendingState = "pending"
	Held         PendingState = "held"
	Ready        PendingState = "ready"
	Dismissed    PendingState = "dismissed"
	Communicated PendingState = "communicated"
)

// DeliveryReceipt is the host's observation of one completion attempt. It is
// accepted only when its identity exactly matches the claimed lease.
type DeliveryReceipt struct {
	Scope             WorkScope `json:"scope"`
	AttemptID         string    `json:"attempt_id"`
	TurnID            string    `json:"turn_id"`
	CaptureEpoch      uint64    `json:"capture_epoch"`
	Mode              string    `json:"mode"`
	ResponseCommitted bool      `json:"response_committed"`
	PlaybackComplete  bool      `json:"playback_complete"`
	Audible           bool      `json:"audible"`
	Interrupted       bool      `json:"interrupted"`
	Segments          int       `json:"segments"`
}

type DeliveryEvidence struct {
	Scope        WorkScope       `json:"scope"`
	AttemptID    string          `json:"attempt_id"`
	TurnID       string          `json:"turn_id"`
	CaptureEpoch uint64          `json:"capture_epoch"`
	Mode         string          `json:"mode"`
	ResultID     string          `json:"result_id"`
	Addressed    bool            `json:"addressed"`
	Reason       string          `json:"reason,omitempty"`
	Receipt      DeliveryReceipt `json:"receipt"`
}

const (
	AnnouncementPending      = "pending"
	AnnouncementScheduled    = "scheduled"
	AnnouncementHeld         = "held"
	AnnouncementCommunicated = "communicated"
)

type ResultView struct {
	ReceivedRevision  uint64            `json:"received_revision,omitempty"`
	Outcome           Outcome           `json:"outcome"`
	Task              string            `json:"task,omitempty"`
	State             PendingState      `json:"state"`
	DeliveryKey       JobKey            `json:"delivery_key"`
	AnnouncementState string            `json:"announcement_state"`
	HoldReason        string            `json:"hold_reason,omitempty"`
	LastAttempt       *DeliveryEvidence `json:"last_attempt,omitempty"`
}

type Snapshot struct {
	Scope             WorkScope    `json:"scope"`
	Revision          uint64       `json:"state_revision"`
	PersistedRevision uint64       `json:"persisted_watermark"`
	Retired           bool         `json:"retired"`
	Jobs              []JobView    `json:"jobs"`
	Pending           []ResultView `json:"pending"`
	Communicated      []ResultView `json:"communicated,omitempty"`
	RetiredJobIDs     []string     `json:"retired_job_ids,omitempty"`
}

type Option func(*Coordinator)

func WithPendingLimit(n int) Option {
	return func(c *Coordinator) {
		if n > 0 {
			c.pendingLimit = n
		}
	}
}
func WithChangeCallback(fn func(Snapshot)) Option { return func(c *Coordinator) { c.onChange = fn } }

type Coordinator struct {
	mu                    sync.Mutex
	scope                 WorkScope
	retired               bool
	revision, persisted   uint64
	nextID                uint64
	jobs                  map[string]*job
	results               map[string]*result
	communicated          map[string]ResultView
	communicatedOrder     []string
	pendingLimit          int
	reserved              int
	onChange              func(Snapshot)
	ready                 chan struct{}
	tombstones            map[string]struct{}
	tombstoneOrder        []string
	permit                *activePermit
	activeLease           *leaseState
	nextPermit, nextLease uint64
}
type job struct{ view JobView }
type result struct{ view ResultView }

func NewCoordinator(scope WorkScope, opts ...Option) *Coordinator {
	c := &Coordinator{scope: scope, jobs: make(map[string]*job), results: make(map[string]*result), communicated: make(map[string]ResultView), pendingLimit: 32, ready: make(chan struct{}, 1), tombstones: make(map[string]struct{})}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Ready is a level-trigger hint; callers must inspect Snapshot/Pending after
// receiving it. It never blocks a state transition.
func (c *Coordinator) Ready() <-chan struct{} { return c.ready }

func (c *Coordinator) Scope() WorkScope { c.mu.Lock(); defer c.mu.Unlock(); return c.scope }

func (c *Coordinator) Register(scope WorkScope, a Admission) (JobKey, error) {
	c.mu.Lock()
	if err := c.checkScopeLocked(scope); err != nil {
		c.mu.Unlock()
		return JobKey{}, err
	}
	if c.reserved >= c.pendingLimit {
		c.mu.Unlock()
		return JobKey{}, ErrOverflow
	}
	id := a.JobID
	if id == "" {
		for {
			c.nextID++
			id = fmt.Sprintf("work-%d", c.nextID)
			if _, exists := c.jobs[id]; exists {
				continue
			}
			if _, retired := c.tombstones[id]; retired || c.communicatedJobLocked(id) {
				continue
			}
			break
		}
	}
	if _, ok := c.jobs[id]; ok {
		c.mu.Unlock()
		return JobKey{}, fmt.Errorf("job %q already exists", id)
	}
	if _, ok := c.tombstones[id]; ok || c.communicatedJobLocked(id) {
		c.mu.Unlock()
		return JobKey{}, fmt.Errorf("job %q was recently retired", id)
	}
	c.jobs[id] = &job{view: JobView{Key: JobKey{Scope: scope, JobID: id}, Task: a.Task, Origin: a.Origin, Provider: a.Provider, Model: a.Model, Config: cloneMap(a.Config), State: JobQueued}}
	c.reserved++
	c.revision++
	s := c.snapshotLocked()
	cb := c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return JobKey{Scope: scope, JobID: id}, nil
}

func (c *Coordinator) Started(key JobKey) error {
	c.mu.Lock()
	if err := c.checkScopeLocked(key.Scope); err != nil {
		c.mu.Unlock()
		return err
	}
	j, ok := c.jobs[key.JobID]
	if !ok {
		c.mu.Unlock()
		return ErrUnknown
	}
	if j.view.State == JobTerminal {
		c.mu.Unlock()
		return nil
	}
	j.view.State = JobRunning
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

func (c *Coordinator) Publish(key JobKey, o Outcome) (ResultView, error) {
	c.mu.Lock()
	if err := c.checkScopeLocked(key.Scope); err != nil {
		c.mu.Unlock()
		return ResultView{}, err
	}
	j, ok := c.jobs[key.JobID]
	if !ok {
		c.mu.Unlock()
		return ResultView{}, ErrUnknown
	}
	if o.Scope != key.Scope || o.JobID != key.JobID {
		c.mu.Unlock()
		return ResultView{}, ErrStaleScope
	}
	if (o.Origin != "" && o.Origin != j.view.Origin) || (o.Provider != "" && o.Provider != j.view.Provider) || (o.Model != "" && o.Model != j.view.Model) || (o.Config != nil && !mapsEqual(o.Config, j.view.Config)) {
		c.mu.Unlock()
		return ResultView{}, ErrConflict
	}
	o.Origin, o.Provider, o.Model, o.Config = j.view.Origin, j.view.Provider, j.view.Model, cloneMap(j.view.Config)
	if o.TerminalRevision == 0 {
		if j.view.State == JobTerminal {
			o.TerminalRevision = j.view.TerminalRevision
		} else {
			o.TerminalRevision = j.view.TerminalRevision + 1
		}
	}
	if o.ResultID == "" {
		o.ResultID = fmt.Sprintf("%s-result-%d", key.JobID, o.TerminalRevision)
	}
	if _, exists := c.communicated[o.ResultID]; exists {
		c.mu.Unlock()
		return ResultView{}, ErrConflict
	}
	if prior, exists := c.results[o.ResultID]; exists && prior.view.Outcome.JobID != key.JobID {
		c.mu.Unlock()
		return ResultView{}, ErrConflict
	}
	if j.view.State == JobTerminal {
		if j.view.Outcome != nil && sameOutcome(*j.view.Outcome, o) {
			v := c.results[j.view.Outcome.ResultID].view
			c.mu.Unlock()
			return cloneResult(v), nil
		}
		c.mu.Unlock()
		return ResultView{}, ErrConflict
	}
	if o.EndedAt.IsZero() {
		o.EndedAt = time.Now()
	}
	if o.Elapsed == 0 && !o.StartedAt.IsZero() {
		o.Elapsed = o.EndedAt.Sub(o.StartedAt)
	}
	j.view.State, j.view.TerminalRevision, j.view.Outcome = JobTerminal, o.TerminalRevision, ptrOutcome(o)
	rv := ResultView{Outcome: o, Task: j.view.Task, State: Pending, DeliveryKey: key, AnnouncementState: AnnouncementPending, ReceivedRevision: c.revision + 1}
	c.results[o.ResultID] = &result{view: rv}
	c.revision++
	s := c.snapshotLocked()
	cb := c.onChange
	c.mu.Unlock()
	notify(cb, s)
	notifyReady(c)
	return cloneResult(rv), nil
}

func (c *Coordinator) Pending(scope WorkScope) []ResultView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sameScopeLocked(scope) {
		return nil
	}
	out := make([]ResultView, 0, len(c.results))
	ids := make([]string, 0, len(c.results))
	for id := range c.results {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := c.results[ids[i]].view, c.results[ids[j]].view
		if a.ReceivedRevision != b.ReceivedRevision {
			return a.ReceivedRevision < b.ReceivedRevision
		}
		if !a.Outcome.EndedAt.Equal(b.Outcome.EndedAt) {
			return a.Outcome.EndedAt.Before(b.Outcome.EndedAt)
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		r := c.results[id]
		if r.view.State != Dismissed {
			out = append(out, cloneResult(r.view))
		}
	}
	return out
}

func (c *Coordinator) Hold(scope WorkScope, resultID string) error {
	return c.HoldWithReason(scope, resultID, "user_requested")
}
func (c *Coordinator) Resume(scope WorkScope, resultID string) error {
	return c.setPending(scope, resultID, Ready, false)
}
func (c *Coordinator) Dismiss(scope WorkScope, resultID string) error {
	return c.setPending(scope, resultID, Dismissed, false)
}

func (c *Coordinator) setPending(scope WorkScope, id string, state PendingState, previous bool) error {
	c.mu.Lock()
	if err := c.checkScopeLocked(scope); err != nil {
		c.mu.Unlock()
		return err
	}
	r, ok := c.results[id]
	if !ok {
		c.mu.Unlock()
		return ErrUnknown
	}
	if r.view.DeliveryKey.Scope != scope {
		c.mu.Unlock()
		return ErrStaleScope
	}
	if r.view.State == Dismissed && state != Dismissed {
		c.mu.Unlock()
		return ErrConflict
	}
	if c.activeLease != nil && r.view.State != state && leaseContainsResult(c.activeLease, id) {
		// A disposition change invalidates an attempt that selected this result.
		// The result itself remains governed by the requested disposition.
		c.recordAbandonedLeaseLocked("disposition_changed")
		c.invalidateCaptureLocked()
	}
	r.view.State = state
	if state == Held {
		r.view.AnnouncementState, r.view.HoldReason = AnnouncementHeld, "user_requested"
	} else if state == Ready {
		r.view.AnnouncementState, r.view.HoldReason = AnnouncementPending, ""
	}
	if previous {
		r.view.Outcome.PreviousSession = true
	}
	if state == Dismissed {
		delete(c.results, id)
		c.reserved--
		jobID := r.view.Outcome.JobID
		delete(c.jobs, jobID)
		c.tombstones[jobID] = struct{}{}
		c.tombstoneOrder = append(c.tombstoneOrder, jobID)
		c.trimTombstonesLocked()
	}
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	if state == Ready {
		notifyReady(c)
	}
	return nil
}

func (c *Coordinator) trimTombstonesLocked() {
	for len(c.tombstoneOrder) > c.pendingLimit {
		old := c.tombstoneOrder[0]
		c.tombstoneOrder = c.tombstoneOrder[1:]
		delete(c.tombstones, old)
	}
}

func leaseContainsResult(l *leaseState, resultID string) bool {
	if l == nil {
		return false
	}
	for _, ref := range l.refs {
		if ref.ResultID == resultID {
			return true
		}
	}
	return false
}

func notifyReady(c *Coordinator) {
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

func (c *Coordinator) AcknowledgePersisted(scope WorkScope, revision uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sameScopeLocked(scope) {
		return ErrStaleScope
	}
	if revision > c.revision {
		return fmt.Errorf("persistence revision %d exceeds current %d", revision, c.revision)
	}
	if revision > c.persisted {
		c.persisted = revision
	}
	return nil
}

func (c *Coordinator) Snapshot(scope WorkScope) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sameScopeLocked(scope) {
		return Snapshot{}
	}
	return c.snapshotLocked()
}

// Restore validates and installs durable state into an already-selected fresh
// scope. Historic results retain Outcome.Scope but receive current delivery
// keys and are held; queued/running jobs become interrupted held results.
func (c *Coordinator) Restore(s Snapshot, destination WorkScope) error {
	if !destination.Valid() || !s.Scope.Valid() || destination.SessionID != s.Scope.SessionID || s.PersistedRevision > s.Revision {
		return ErrInvalid
	}
	if len(s.RetiredJobIDs) > c.pendingLimit || len(s.Communicated) > c.pendingLimit {
		return ErrOverflow
	}
	retiredIDs := make(map[string]struct{}, len(s.RetiredJobIDs))
	for _, id := range s.RetiredJobIDs {
		if id == "" {
			return ErrInvalid
		}
		if _, exists := retiredIDs[id]; exists {
			return ErrInvalid
		}
		retiredIDs[id] = struct{}{}
	}
	jobs := make(map[string]*job)
	results := make(map[string]*result)
	incomingResultIDs := make(map[string]struct{}, len(s.Pending))
	for _, v := range s.Pending {
		if v.Outcome.ResultID == "" {
			return ErrInvalid
		}
		if _, exists := incomingResultIDs[v.Outcome.ResultID]; exists {
			return ErrInvalid
		}
		incomingResultIDs[v.Outcome.ResultID] = struct{}{}
	}
	communicatedJobs := make(map[string]bool)
	communicated := make([]ResultView, 0, len(s.Communicated))
	for _, v := range s.Communicated {
		if communicatedJobs[v.Outcome.JobID] || !validCommunicatedEvidence(v, s.Scope.SessionID) {
			return ErrInvalid
		}
		communicatedJobs[v.Outcome.JobID] = true
		if v.Outcome.ResultID == "" || v.Outcome.JobID == "" || v.DeliveryKey.Scope != s.Scope || v.DeliveryKey.JobID != v.Outcome.JobID || !v.Outcome.Scope.Valid() || v.Outcome.Scope.SessionID != s.Scope.SessionID || !validOutcomeStatus(v.Outcome.Status) || v.State != Communicated || v.AnnouncementState != AnnouncementCommunicated {
			return ErrInvalid
		}
		if _, exists := incomingResultIDs[v.Outcome.ResultID]; exists {
			return ErrInvalid
		}
		incomingResultIDs[v.Outcome.ResultID] = struct{}{}
		communicated = append(communicated, cloneResult(v))
	}
	for _, j := range s.Jobs {
		if j.Key.JobID == "" || j.Key.Scope != s.Scope || jobs[j.Key.JobID] != nil || communicatedJobs[j.Key.JobID] || !validJobState(j.State) {
			return ErrInvalid
		}
		j.Key.Scope = destination
		j.Config = cloneMap(j.Config)
		if j.State == JobTerminal && (j.Outcome == nil || j.Outcome.ResultID == "") {
			return ErrInvalid
		}
		if j.State != JobTerminal && j.Outcome != nil {
			return ErrInvalid
		}
		if j.Outcome != nil {
			o := *j.Outcome
			if o.JobID != j.Key.JobID || !o.Scope.Valid() || o.Scope.SessionID != s.Scope.SessionID || !validOutcomeStatus(o.Status) {
				return ErrInvalid
			}
			o.Config = cloneMap(o.Config)
			j.Outcome = &o
		}
		if j.State == JobQueued || j.State == JobRunning {
			resultID := j.Key.JobID + "-interrupted"
			for n := 2; ; n++ {
				if _, incoming := incomingResultIDs[resultID]; !incoming {
					if _, generated := results[resultID]; !generated {
						break
					}
				}
				resultID = fmt.Sprintf("%s-interrupted-%d", j.Key.JobID, n)
			}
			o := Outcome{ResultID: resultID, Scope: s.Scope, JobID: j.Key.JobID, TerminalRevision: 1, Status: OutcomeInterrupted, Text: "Work was interrupted by the previous session.", Origin: j.Origin, Provider: j.Provider, Model: j.Model, Config: cloneMap(j.Config), Termination: "unknown", PreviousSession: true}
			j.State, j.TerminalRevision, j.Outcome = JobTerminal, 1, &o
			results[o.ResultID] = &result{view: ResultView{Outcome: o, Task: j.Task, State: Held, DeliveryKey: JobKey{Scope: destination, JobID: j.Key.JobID}, AnnouncementState: AnnouncementHeld, HoldReason: "previous_session"}}
		}
		jobs[j.Key.JobID] = &job{view: j}
	}
	for id := range jobs {
		if _, exists := retiredIDs[id]; exists {
			return ErrInvalid
		}
	}
	for _, v := range s.Pending {
		v = cloneResult(v)
		if v.Outcome.JobID == "" || v.DeliveryKey.JobID != v.Outcome.JobID || results[v.Outcome.ResultID] != nil || v.DeliveryKey.Scope != s.Scope || jobs[v.Outcome.JobID] == nil || jobs[v.Outcome.JobID].view.State != JobTerminal || jobs[v.Outcome.JobID].view.Outcome == nil || !samePersistedOutcome(*jobs[v.Outcome.JobID].view.Outcome, v.Outcome) || !v.Outcome.Scope.Valid() || v.Outcome.Scope.SessionID != s.Scope.SessionID || !validOutcomeStatus(v.Outcome.Status) {
			return ErrInvalid
		}
		v.Task = jobs[v.Outcome.JobID].view.Task
		v.DeliveryKey = JobKey{Scope: destination, JobID: v.Outcome.JobID}
		v.State = Held
		v.AnnouncementState = AnnouncementHeld
		v.HoldReason = "previous_session"
		v.Outcome.PreviousSession = true
		jobs[v.Outcome.JobID].view.Outcome.PreviousSession = true
		results[v.Outcome.ResultID] = &result{view: v}
	}
	live := 0
	for _, j := range jobs {
		if j.view.State != JobTerminal {
			live++
		}
	}
	if len(results)+live > c.pendingLimit || len(jobs) > c.pendingLimit {
		return ErrOverflow
	}
	c.mu.Lock()
	if destination == c.scope {
		c.mu.Unlock()
		return ErrConflict
	}
	c.invalidateCaptureLocked()
	c.scope, c.retired = destination, false
	c.revision, c.persisted = s.Revision, s.PersistedRevision
	c.jobs, c.results, c.reserved = jobs, results, len(results)+live
	c.communicated = make(map[string]ResultView, len(communicated))
	c.communicatedOrder = nil
	for _, v := range communicated {
		v = cloneResult(v)
		v.DeliveryKey = JobKey{Scope: destination, JobID: v.Outcome.JobID}
		c.communicated[v.Outcome.ResultID] = v
		c.communicatedOrder = append(c.communicatedOrder, v.Outcome.ResultID)
	}
	c.trimCommunicatedLocked()
	c.tombstones = make(map[string]struct{}, len(s.RetiredJobIDs))
	c.tombstoneOrder = append([]string(nil), s.RetiredJobIDs...)
	for _, id := range s.RetiredJobIDs {
		c.tombstones[id] = struct{}{}
	}
	snap, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, snap)
	notifyReady(c)
	return nil
}

func validJobState(s JobState) bool { return s == JobQueued || s == JobRunning || s == JobTerminal }
func validOutcomeStatus(s OutcomeStatus) bool {
	return s == OutcomeSucceeded || s == OutcomePartial || s == OutcomeFailed || s == OutcomeCancelled || s == OutcomeTimedOut || s == OutcomeInterrupted
}

func samePersistedOutcome(a, b Outcome) bool {
	return a.ResultID == b.ResultID && a.Scope == b.Scope && a.JobID == b.JobID && a.TerminalRevision == b.TerminalRevision && a.Status == b.Status && a.Text == b.Text && a.Origin == b.Origin && a.Provider == b.Provider && a.Model == b.Model && mapsEqual(a.Config, b.Config) && a.SourceRef == b.SourceRef && a.Limitations == b.Limitations && a.Termination == b.Termination && a.StartedAt.Equal(b.StartedAt) && a.EndedAt.Equal(b.EndedAt) && a.Elapsed == b.Elapsed && a.PreviousSession == b.PreviousSession
}

func (c *Coordinator) Retire(scope WorkScope) error {
	c.mu.Lock()
	if err := c.checkScopeLocked(scope); err != nil {
		c.mu.Unlock()
		return err
	}
	c.recordAbandonedLeaseLocked("conversation_retired")
	c.invalidateCaptureLocked()
	for id, j := range c.jobs {
		if j.view.State == JobTerminal {
			continue
		}
		revision := j.view.TerminalRevision + 1
		resultID := id + "-result-" + fmt.Sprint(revision)
		for n := uint64(2); ; n++ {
			if _, exists := c.results[resultID]; !exists {
				break
			}
			resultID = id + "-result-" + fmt.Sprint(revision) + "-retired-" + fmt.Sprint(n)
		}
		o := Outcome{ResultID: resultID, Scope: scope, JobID: id, TerminalRevision: revision, Status: OutcomeInterrupted, Text: "Conversation ended before work completion was confirmed.", Origin: j.view.Origin, Provider: j.view.Provider, Model: j.view.Model, Config: cloneMap(j.view.Config), Termination: "unknown"}
		j.view.State, j.view.TerminalRevision, j.view.Outcome = JobTerminal, revision, ptrOutcome(o)
		c.results[resultID] = &result{view: ResultView{Outcome: o, Task: j.view.Task, ReceivedRevision: c.revision + 1, State: Pending, DeliveryKey: JobKey{Scope: scope, JobID: id}, AnnouncementState: AnnouncementPending}}
	}
	c.retired = true
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

// Reset fences the old scope and starts the explicitly supplied new scope.
func (c *Coordinator) Reset(next WorkScope) error {
	if !next.Valid() {
		return ErrStaleScope
	}
	c.mu.Lock()
	if next == c.scope {
		c.mu.Unlock()
		return ErrConflict
	}
	c.invalidateCaptureLocked()
	c.scope, c.retired, c.revision, c.persisted = next, false, 0, 0
	c.jobs, c.results = make(map[string]*job), make(map[string]*result)
	c.communicated, c.communicatedOrder = make(map[string]ResultView), nil
	c.reserved, c.nextID = 0, 0
	c.tombstones, c.tombstoneOrder = make(map[string]struct{}), nil
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	return nil
}

func (c *Coordinator) checkScopeLocked(s WorkScope) error {
	if !c.sameScopeLocked(s) {
		return ErrStaleScope
	}
	if c.retired {
		return ErrRetired
	}
	return nil
}

func (c *Coordinator) sameScopeLocked(s WorkScope) bool { return s == c.scope }
func (c *Coordinator) snapshotLocked() Snapshot {
	s := Snapshot{Scope: c.scope, Revision: c.revision, PersistedRevision: c.persisted, Retired: c.retired, Jobs: make([]JobView, 0, len(c.jobs)), Pending: make([]ResultView, 0, len(c.results)), Communicated: make([]ResultView, 0, len(c.communicated))}
	jobIDs := make([]string, 0, len(c.jobs))
	for id := range c.jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	for _, id := range jobIDs {
		j := c.jobs[id]
		jv := j.view
		jv.Config = cloneMap(jv.Config)
		if jv.Outcome != nil {
			o := *jv.Outcome
			o.Config = cloneMap(o.Config)
			jv.Outcome = &o
		}
		s.Jobs = append(s.Jobs, jv)
	}
	resultIDs := make([]string, 0, len(c.results))
	for id := range c.results {
		resultIDs = append(resultIDs, id)
	}
	sort.Strings(resultIDs)
	for _, id := range resultIDs {
		r := c.results[id]
		s.Pending = append(s.Pending, cloneResult(r.view))
	}
	communicatedIDs := make([]string, 0, len(c.communicated))
	for id := range c.communicated {
		communicatedIDs = append(communicatedIDs, id)
	}
	sort.Strings(communicatedIDs)
	for _, id := range communicatedIDs {
		s.Communicated = append(s.Communicated, cloneResult(c.communicated[id]))
	}
	s.RetiredJobIDs = append([]string(nil), c.tombstoneOrder...)
	return s
}
func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	n := make(map[string]string, len(m))
	for k, v := range m {
		n[k] = v
	}
	return n
}
func ptrOutcome(o Outcome) *Outcome { return &o }
func cloneResult(v ResultView) ResultView {
	o := v.Outcome
	o.Config = cloneMap(o.Config)
	out := v
	out.Outcome = o
	if v.LastAttempt != nil {
		e := *v.LastAttempt
		out.LastAttempt = &e
	}
	return out
}
func sameOutcome(a, b Outcome) bool {
	if a.ResultID != b.ResultID || a.Scope != b.Scope || a.JobID != b.JobID || a.TerminalRevision != b.TerminalRevision || a.Status != b.Status || a.Text != b.Text || a.Origin != b.Origin || a.Provider != b.Provider || a.Model != b.Model || a.SourceRef != b.SourceRef || a.Limitations != b.Limitations || a.Termination != b.Termination || !mapsEqual(a.Config, b.Config) {
		return false
	}
	if !b.StartedAt.IsZero() && a.StartedAt != b.StartedAt {
		return false
	}
	if !b.EndedAt.IsZero() && a.EndedAt != b.EndedAt {
		return false
	}
	if b.Elapsed != 0 && a.Elapsed != b.Elapsed {
		return false
	}
	return true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func notify(cb func(Snapshot), s Snapshot) {
	if cb != nil {
		cb(s)
	}
}
