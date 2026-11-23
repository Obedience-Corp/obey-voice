package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// Refusals a correction can get from a worker that cannot take one.
const (
	steerNoInput = "this worker cannot take a correction while it runs"
	steerPaid    = "a paid worker cannot take a correction while it runs"
	steerMoved   = "a reply moved to the background cannot take a correction"
	steerManaged = "a managed delegation cannot take a correction"
	steerOther   = "this job cannot take a correction"
)

// jobSteer is what a correction to one spawned job reaches: the job's brief
// until its worker takes it, then the worker's control when the worker can
// take input. The run closure and the registry share it from admission on.
type jobSteer struct {
	id string
	a  *Agency

	mu      sync.Mutex
	started bool
	closed  bool
	briefed []string
	control *brain.WorkerControl
	noInput string
	log     []brain.Correction
	// checkpoint is the revision of the last worker thread saved, across
	// every attempt of the job.
	checkpoint uint64
}

type jobSteerKey struct{}

func withJobSteer(ctx context.Context, s *jobSteer) context.Context {
	return context.WithValue(ctx, jobSteerKey{}, s)
}

func jobSteerFrom(ctx context.Context) *jobSteer {
	s, _ := ctx.Value(jobSteerKey{}).(*jobSteer)
	return s
}

// registerSteer makes a spawned job reachable by corrections. It runs in the
// job's accept callback, before the job can start or be named.
func (a *Agency) registerSteer(id string, s *jobSteer) {
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.steers == nil {
		a.steers = make(map[string]*jobSteer)
	}
	a.steers[id] = s
}

func (a *Agency) steerFor(id string) *jobSteer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.steers[id]
}

// begin is the worker taking its first input: the brief gains every queued
// correction exactly once, and a worker that reads a control while it runs
// gets one. It returns the brief to send and the control, if any.
func (s *jobSteer) begin(brief string, worker brain.Provider) (string, *brain.WorkerControl) {
	if s == nil {
		return brief, nil
	}
	s.mu.Lock()
	s.started = true
	if s.closed {
		s.mu.Unlock()
		return brief, nil
	}
	briefed := append([]string(nil), s.briefed...)
	var included []brain.Correction
	for i := range s.log {
		s.log[i].State = brain.CorrectionIncluded
		included = append(included, s.log[i])
	}
	if s.noInput == "" && brain.TakesCorrections(worker) {
		s.control = brain.NewWorkerControl(s.record, len(briefed))
	} else if s.noInput == "" {
		s.noInput = steerNoInput
	}
	control := s.control
	s.mu.Unlock()
	for _, c := range included {
		s.a.emitCorrection(s.id, c, false)
	}
	return briefWithCorrections(brief, briefed), control
}

// briefWithCorrections labels each correction given before the job started.
func briefWithCorrections(brief string, corrections []string) string {
	var b strings.Builder
	b.WriteString(brief)
	for _, text := range corrections {
		b.WriteString("\n\nCorrection from the user: ")
		b.WriteString(text)
	}
	return b.String()
}

// refuseInput marks a job whose worker will not read corrections, before it
// starts.
func (s *jobSteer) refuseInput(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noInput == "" {
		s.noInput = reason
	}
}

// corrected reports a correction the worker has been sent, in its brief or
// in a request, which may have changed what the task asks for.
func (s *jobSteer) corrected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	briefed, control := len(s.briefed), s.control
	s.mu.Unlock()
	return briefed > 0 || (control != nil && control.Included() > 0)
}

// record keeps a control's correction for /workers and reports it.
func (s *jobSteer) record(c brain.Correction) {
	s.mu.Lock()
	replaced := false
	for i := range s.log {
		if s.log[i].Revision == c.Revision {
			s.log[i], replaced = c, true
		}
	}
	if !replaced {
		s.log = append(s.log, c)
	}
	s.mu.Unlock()
	s.a.emitCorrection(s.id, c, false)
}

// end closes the job to corrections. A cancel supersedes what was not yet
// sent; any other end means it could not apply, for reason.
func (s *jobSteer) end(cancelled bool, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	control := s.control
	state := brain.CorrectionNotApplied
	if cancelled {
		state, reason = brain.CorrectionSuperseded, ""
	}
	var settled []brain.Correction
	if !s.started {
		for i := range s.log {
			s.log[i].State, s.log[i].Reason = state, reason
			settled = append(settled, s.log[i])
		}
	}
	s.mu.Unlock()
	for _, c := range settled {
		s.a.emitCorrection(s.id, c, false)
	}
	switch {
	case control == nil:
	case cancelled:
		control.Supersede()
	default:
		control.Close(reason)
	}
}

// saveCheckpoint saves the worker's thread as the job's newest evidence. It
// runs on the worker's goroutine, after history holds a complete step.
func (s *jobSteer) saveCheckpoint(worker brain.Provider) {
	turns := worker.History()
	if len(turns) == 0 {
		return
	}
	s.mu.Lock()
	s.checkpoint++
	revision := s.checkpoint
	s.mu.Unlock()
	s.a.saveWorkerThreadAt(s.id, turns, revision)
}

func (a *Agency) newJobSteer() *jobSteer { return &jobSteer{a: a} }

// label is how the job's newest correction reads in /workers.
func (s *jobSteer) label() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.log) == 0 {
		return ""
	}
	return CorrectionLabel(s.log[len(s.log)-1], !s.started)
}

// steerJob handles a correction. Only a command may give one for now; the
// worker's control, not the job table, decides whether it is in time.
func (a *Agency) steerJob(receipt *WorkReceipt) {
	req := receipt.Request
	text := strings.TrimSpace(req.Instruction)
	switch {
	case receipt.Actor.Kind != WorkActorCommand:
		receipt.Rejected = "only a command can correct a job"
	case req.All:
		receipt.Rejected = "a correction names one job"
	case req.JobID == "":
		receipt.Rejected = "no job named"
	case text == "":
		receipt.Rejected = "no correction given"
	default:
		receipt.Jobs = append(receipt.Jobs, a.steerOne(req.JobID, text))
	}
}

func (a *Agency) steerOne(id, text string) JobControl {
	if a.managedOwner(id) != nil {
		return JobControl{ID: id, Managed: true, Outcome: ControlRejected, Reason: steerManaged}
	}
	if !a.jobInConversation(id) {
		return JobControl{ID: id, Outcome: ControlUnknown}
	}
	if s := a.steerFor(id); s != nil {
		return s.add(text)
	}
	for _, info := range a.jobs.Status() {
		if info.ID != id {
			continue
		}
		if info.State != job.StateQueued && info.State != job.StateRunning {
			return JobControl{ID: id, Outcome: ControlFinished}
		}
		reason := steerOther
		a.mu.Lock()
		if a.workerOrigins[id].continued {
			reason = steerMoved
		}
		a.mu.Unlock()
		return JobControl{ID: id, Outcome: ControlRejected, Reason: reason}
	}
	return JobControl{ID: id, Outcome: ControlUnknown}
}

func (s *jobSteer) add(text string) JobControl {
	item := JobControl{ID: s.id}
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		item.Outcome = ControlFinished
		return item
	case !s.started:
		s.briefed = append(s.briefed, text)
		c := brain.Correction{Revision: len(s.briefed), Text: text, State: brain.CorrectionPending}
		s.log = append(s.log, c)
		s.mu.Unlock()
		s.a.emitCorrection(s.id, c, true)
		item.Outcome, item.Revision = ControlBriefed, c.Revision
		return item
	case s.control == nil:
		item.Outcome, item.Reason = ControlRejected, s.noInput
		s.mu.Unlock()
		return item
	}
	control := s.control
	s.mu.Unlock()
	c := control.Steer(text)
	if c.State == brain.CorrectionRefused {
		item.Outcome = ControlFinished
		return item
	}
	item.Outcome, item.Revision = ControlAccepted, c.Revision
	return item
}

// endSteer closes a job to corrections when it ends, however it ends.
func (a *Agency) endSteer(id string, cancelled bool, reason string) {
	a.steerFor(id).end(cancelled, reason)
}

// steerEndReason is why a job's open corrections could not apply when the
// table ended it.
func steerEndReason(state job.State, err error) string {
	switch {
	case errors.Is(err, job.ErrBudget):
		return brain.NotAppliedLimit
	case state == job.StateFailed:
		return brain.NotAppliedFailed
	default:
		return brain.NotAppliedStopped
	}
}

func (a *Agency) emitCorrection(id string, c brain.Correction, brief bool) {
	if a.pipeline == nil {
		return
	}
	_, key, _ := a.callbackKey(id)
	a.emitJobTrace(id, events.JobCorrection{Scope: key.Scope, ID: id, Revision: c.Revision, Text: c.Text, State: string(c.State), Reason: c.Reason, Brief: brief})
}

// TellWork gives one job a correction from the keyboard. It never waits for
// the worker: when the correction reaches a request, an event says so.
func (p *Pipeline) TellWork(id, instruction string) WorkReceipt {
	if p == nil || p.Agency == nil {
		return WorkReceipt{Rejected: "background work is unavailable"}
	}
	return p.Agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: id, Instruction: instruction})
}

// CorrectionLabel is how a job's newest correction reads on its row.
func CorrectionLabel(c brain.Correction, brief bool) string {
	switch c.State {
	case brain.CorrectionPending:
		if brief {
			return "correction added to brief"
		}
		return "correction pending"
	case brain.CorrectionIncluded:
		return "correction included"
	case brain.CorrectionNotApplied:
		if c.Reason == "" {
			return "correction could not apply"
		}
		return fmt.Sprintf("correction could not apply: %s", c.Reason)
	case brain.CorrectionSuperseded:
		return "correction superseded by cancel"
	case brain.CorrectionRefused:
		return "correction refused: already finished"
	}
	return ""
}
