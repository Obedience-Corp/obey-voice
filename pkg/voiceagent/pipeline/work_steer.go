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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Refusals a correction can get from a job that cannot take one.
const (
	steerNoInput = "this worker cannot take a correction while it runs"
	steerPaid    = "a paid worker cannot take a correction while it runs"
	steerRemote  = "a worker not served on this machine cannot take a correction while it runs"
	steerUnsure  = "this worker cannot take a correction: whether it runs on this machine could not be checked"
	steerMoved   = "a reply moved to the background cannot take a correction"
	steerManaged = "a managed delegation cannot take a correction"
	steerOther   = "this job cannot take a correction"
)

// jobSteer is a spawned job's correction state from admission to its end:
// the run closure and the registry share it. Its control holds the
// corrections; the job table's ending of the job settles them.
type jobSteer struct {
	a       *Agency
	control *brain.WorkerControl

	// scope is the conversation that admitted the job. Corrections come only
	// from it, and their events carry it.
	scope work.WorkScope

	mu sync.Mutex
	id string
	// noInput, set before the worker starts, is why it will not read
	// corrections while it runs.
	noInput string
	// local marks a worker whose admission already proved it runs on this
	// machine: a tiered local worker.
	local bool
	// checkpoint is the revision of the last worker thread saved, across
	// every run of the job.
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

func (a *Agency) newJobSteer(scope work.WorkScope) *jobSteer {
	s := &jobSteer{a: a, scope: scope}
	s.control = brain.NewWorkerControl(s.report)
	return s
}

// registerSteer makes a job reachable by corrections. It runs before the
// job's id is visible to anyone who could name it.
func (a *Agency) registerSteer(id string, s *jobSteer) {
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
	a.steersMu.Lock()
	defer a.steersMu.Unlock()
	if a.steers == nil {
		a.steers = make(map[string]*jobSteer)
	}
	a.steers[id] = s
}

// unregisterSteer forgets a job that was never admitted.
func (a *Agency) unregisterSteer(id string, s *jobSteer) {
	s.control.Close(brain.NotAppliedStopped)
	a.steersMu.Lock()
	defer a.steersMu.Unlock()
	if a.steers[id] == s {
		delete(a.steers, id)
	}
}

func (a *Agency) steerFor(id string) *jobSteer {
	a.steersMu.Lock()
	defer a.steersMu.Unlock()
	return a.steers[id]
}

// begin is the worker taking its first input: the brief gains the
// corrections given before the start, exactly once. It returns the brief and
// the control when the worker reads corrections while it runs.
func (s *jobSteer) begin(ctx context.Context, brief string, worker brain.Provider) (string, *brain.WorkerControl) {
	if s == nil {
		return brief, nil
	}
	s.mu.Lock()
	noInput, local := s.noInput, s.local
	s.mu.Unlock()
	if noInput == "" && !brain.TakesCorrections(worker) {
		noInput = steerNoInput
	}
	// A model on another machine or behind a cloud alias would spend a
	// correction's extra requests there. The worker itself is asked, with the
	// metadata its job already fetched; the configured worker provider may
	// name another backend than the one a tiered or own job runs on.
	if checker, ok := worker.(interface {
		OnThisMachine(context.Context) (bool, error)
	}); noInput == "" && !local && ok {
		switch onMachine, err := checker.OnThisMachine(ctx); {
		case err != nil:
			noInput = steerUnsure
		case !onMachine:
			noInput = steerRemote
		}
	}
	corrections := s.control.Begin(noInput == "", noInput)
	brief = briefWithCorrections(brief, corrections)
	if noInput != "" {
		return brief, nil
	}
	return brief, s.control
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

// localWorker marks a worker whose admission proved it runs on this machine.
func (s *jobSteer) localWorker() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = true
}

// corrected reports a correction the worker was given, which may have
// changed what the task asks for.
func (s *jobSteer) corrected() bool {
	return s != nil && s.control.Corrected()
}

// runningCorrections are the corrections answered in a request after the job
// started, in order; the ones given before the start are in its brief.
func (s *jobSteer) runningCorrections() []string {
	if s == nil {
		return nil
	}
	var texts []string
	for _, c := range s.control.Corrections() {
		if c.State == brain.CorrectionIncluded && !c.Brief {
			texts = append(texts, c.Text)
		}
	}
	return texts
}

// report publishes a correction's acceptance or new disposition.
func (s *jobSteer) report(c brain.Correction) {
	s.mu.Lock()
	id := s.id
	s.mu.Unlock()
	if id != "" {
		s.a.emitCorrection(id, c, s)
	}
}

// decide settles the job's corrections from the table's decision to end it,
// with the cause that won: a user's cancel supersedes them, a budget stop is
// a limit, any other stop or failure is reported as such. It runs under the
// table's lock, so it reports nothing; flush does, later. A delivered answer
// has already sealed the control.
func (s *jobSteer) decide(state job.State, err error) {
	if s == nil {
		return
	}
	switch {
	case state == job.StateAborted && errors.Is(err, job.ErrBudget):
		s.control.Decide(brain.CorrectionNotApplied, brain.NotAppliedLimit)
	case state == job.StateAborted && errors.Is(err, job.ErrCancelled):
		s.control.Decide(brain.CorrectionSuperseded, "")
	case state == job.StateFailed:
		s.control.Decide(brain.CorrectionNotApplied, brain.NotAppliedReason(err))
	default:
		s.control.Decide(brain.CorrectionNotApplied, brain.NotAppliedStopped)
	}
}

// flush reports the corrections' dispositions decided so far.
func (s *jobSteer) flush() {
	if s != nil {
		s.control.Flush()
	}
}

// saveCheckpoint saves the worker's thread as the job's newest evidence. It
// runs on the worker's goroutine.
func (s *jobSteer) saveCheckpoint(worker brain.Provider) {
	turns := worker.History()
	if len(turns) == 0 {
		return
	}
	s.mu.Lock()
	s.checkpoint++
	revision, id := s.checkpoint, s.id
	s.mu.Unlock()
	s.a.saveWorkerThreadAt(id, turns, revision)
}

// label is how the job's newest correction reads in /workers.
func (s *jobSteer) label() string {
	if s == nil {
		return ""
	}
	all := s.control.Corrections()
	if len(all) == 0 {
		return ""
	}
	return CorrectionLabel(all[len(all)-1])
}

// steerJob handles a correction. Only a command may give one for now; the
// job's control, not the job table, decides whether it is in time.
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
		if !a.inAdmittingConversation(s) {
			return JobControl{ID: id, Outcome: ControlUnknown}
		}
		c := s.control.Steer(text)
		switch c.State {
		case brain.CorrectionRefused:
			return JobControl{ID: id, Outcome: ControlFinished}
		case brain.CorrectionRejected:
			return JobControl{ID: id, Outcome: ControlRejected, Reason: c.Reason}
		case brain.CorrectionPending:
			if c.Brief {
				return JobControl{ID: id, Outcome: ControlBriefed, Revision: c.Revision}
			}
			return JobControl{ID: id, Outcome: ControlAccepted, Revision: c.Revision}
		}
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

// inAdmittingConversation reports whether the current conversation is the one
// that admitted the job, in either work mode. Unlike a cancel, a correction
// never reaches a job a clear left behind.
func (a *Agency) inAdmittingConversation(s *jobSteer) bool {
	if c, scope := a.workAuthority(); c != nil {
		return s.scope == scope && !c.Snapshot(scope).Retired
	}
	if !s.scope.Valid() {
		return true
	}
	return a.pipeline != nil && s.scope == a.pipeline.ConversationScope() && !a.pipeline.ConversationRetired()
}

// emitCorrection publishes a correction event for a job of the current
// conversation, with the scope that admitted it. The check uses that scope,
// which outlives the job's entry in the work inbox; a retired conversation's
// job is settled silently.
func (a *Agency) emitCorrection(id string, c brain.Correction, s *jobSteer) {
	if a.pipeline == nil || !a.inAdmittingConversation(s) {
		return
	}
	a.emitJobTrace(id, events.JobCorrection{Scope: s.scope, ID: id, Revision: c.Revision, Text: c.Text, State: string(c.State), Reason: c.Reason, Brief: c.Brief})
}

// TellWork gives one job a correction from the keyboard. It never waits for
// the worker: when the correction reaches a request, an event says so.
func (p *Pipeline) TellWork(id, instruction string) WorkReceipt {
	if p == nil || p.Agency == nil {
		return WorkReceipt{Rejected: "background work is unavailable"}
	}
	return p.Agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkSteer, JobID: id, Instruction: instruction})
}

// CorrectionLabel is how a correction reads on its job's row.
func CorrectionLabel(c brain.Correction) string {
	switch c.State {
	case brain.CorrectionPending:
		if c.Brief {
			return "correction added to brief"
		}
		return "correction pending"
	case brain.CorrectionBriefed:
		return "correction added to brief"
	case brain.CorrectionIncluded:
		return "correction included"
	case brain.CorrectionNotApplied:
		if c.Reason == "" {
			return "correction could not apply"
		}
		return fmt.Sprintf("correction could not apply: %s", c.Reason)
	case brain.CorrectionSuperseded:
		return "correction superseded by cancel"
	}
	return ""
}
