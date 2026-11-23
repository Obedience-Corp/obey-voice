package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// Refusals a correction can get from a job that cannot take one.
const (
	steerNoInput = "this worker cannot take a correction while it runs"
	steerPaid    = "a paid worker cannot take a correction while it runs"
	steerRemote  = "a worker not served on this machine cannot take a correction while it runs"
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

	mu sync.Mutex
	id string
	// cancelled: a cancel asked the table to end the job, so the corrections
	// it ends are superseded rather than stopped.
	cancelled bool
	// noInput, set before the worker starts, is why it will not read
	// corrections while it runs.
	noInput string
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

func (a *Agency) newJobSteer() *jobSteer {
	s := &jobSteer{a: a}
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
func (s *jobSteer) begin(ctx context.Context, brief string, worker brain.Provider, cfg *config.Config) (string, *brain.WorkerControl) {
	if s == nil {
		return brief, nil
	}
	s.mu.Lock()
	noInput := s.noInput
	s.mu.Unlock()
	if noInput == "" && !brain.TakesCorrections(worker) {
		noInput = steerNoInput
	}
	// A cloud model behind Ollama would bill a correction's extra requests.
	if noInput == "" && !servedLocally(ctx, cfg, worker) {
		noInput = steerRemote
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

// corrected reports a correction the worker was given, which may have
// changed what the task asks for.
func (s *jobSteer) corrected() bool {
	return s != nil && s.control.Corrected()
}

// report publishes a correction's acceptance or new disposition.
func (s *jobSteer) report(c brain.Correction) {
	s.mu.Lock()
	id := s.id
	s.mu.Unlock()
	if id != "" {
		s.a.emitCorrection(id, c)
	}
}

// markCancel records that a cancel is asking the table to end the job.
func (s *jobSteer) markCancel() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cancelled = true
	s.mu.Unlock()
}

// ended settles the job's corrections from the table's ending of it: a
// cancel supersedes them, a budget stop is a limit, any other stop or failure
// is reported as such. A delivered answer has already sealed the control.
func (s *jobSteer) ended(state job.State, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancelled := s.cancelled
	s.mu.Unlock()
	switch {
	case state == job.StateAborted && errors.Is(err, job.ErrBudget):
		s.control.Close(brain.NotAppliedLimit)
	case state == job.StateAborted && cancelled:
		s.control.Supersede()
	case state == job.StateFailed:
		s.control.Close(brain.NotAppliedReason(err))
	default:
		s.control.Close(brain.NotAppliedStopped)
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

// emitCorrection publishes a correction event for a job of the current
// conversation, with its admitted scope. A retired conversation's job is
// settled silently.
func (a *Agency) emitCorrection(id string, c brain.Correction) {
	if a.pipeline == nil {
		return
	}
	_, key, ok := a.callbackKey(id)
	if !ok {
		return
	}
	a.emitJobTrace(id, events.JobCorrection{Scope: key.Scope, ID: id, Revision: c.Revision, Text: c.Text, State: string(c.State), Reason: c.Reason, Brief: c.Brief})
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
