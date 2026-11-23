package pipeline

import (
	"cmp"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// Receipts of a report that the job does not answer.
const (
	reportNoInput    = "this worker cannot be asked to stop, so the work continues"
	reportNotStarted = "the job has not started"
	reportFinishing  = "the job is already finishing its answer"
	reportStarting   = "the job is starting; there is nothing to report yet"
)

// noActivity is a report's checkpoint for a job with no tool activity.
const noActivity = "no tool activity recorded yet"

// Labels a report gives its job in /workers.
const (
	reportRunning = "reporting"
	reportEarly   = "finished before it was asked"
	reportAsked   = "report requested"
)

// reportJob handles a request for a job's report. Only a command may ask for
// one for now; the job's control, not the job table, decides whether the
// worker can still answer it.
func (a *Agency) reportJob(receipt *WorkReceipt) {
	req := receipt.Request
	switch {
	case receipt.Actor.Kind != WorkActorCommand:
		receipt.Rejected = "only a command can ask a job to report"
	case req.All:
		receipt.Rejected = "a report names one job"
	case req.JobID == "":
		receipt.Rejected = "no job named"
	default:
		receipt.Jobs = append(receipt.Jobs, a.reportOne(req.JobID))
	}
}

func (a *Agency) reportOne(id string) JobControl {
	checkpoint := func() string { return cmp.Or(a.work.Summary(id), noActivity) }
	if a.managedOwner(id) != nil {
		return JobControl{ID: id, Managed: true, Outcome: ControlRejected, Reason: reportNoInput, Checkpoint: checkpoint()}
	}
	if !a.jobInConversation(id) {
		return JobControl{ID: id, Outcome: ControlUnknown}
	}
	if s := a.steerFor(id); s != nil {
		if !a.inAdmittingConversation(s) {
			return JobControl{ID: id, Outcome: ControlUnknown}
		}
		return a.reportSteered(id, s, checkpoint)
	}
	// A job with no control, such as a reply moved to the background, gives
	// its activity only to the conversation it came from.
	a.mu.Lock()
	scope := a.workerOrigins[id].Scope
	a.mu.Unlock()
	if scope.Valid() && (scope != a.pipeline.ConversationScope() || a.pipeline.ConversationRetired()) {
		return JobControl{ID: id, Outcome: ControlUnknown}
	}
	switch state := a.jobState(id); state {
	case "":
		return JobControl{ID: id, Outcome: ControlUnknown}
	case job.StateQueued:
		return JobControl{ID: id, Outcome: ControlRejected, Queued: true, Reason: reportNotStarted}
	case job.StateRunning:
		return JobControl{ID: id, Outcome: ControlRejected, Reason: reportNoInput, Checkpoint: checkpoint()}
	}
	return JobControl{ID: id, Outcome: ControlFinished}
}

// reportSteered asks a job that has a control: the control, not the job
// table, decides whether its worker can still answer.
func (a *Agency) reportSteered(id string, s *jobSteer, checkpoint func() string) JobControl {
	got, kept := s.control.Report(checkpoint)
	item := JobControl{ID: id, Outcome: ControlRejected, Checkpoint: kept}
	switch got {
	case brain.ReportAccepted:
		item.Outcome = ControlAccepted
		a.emitReport(id, s, kept, true)
		// The corrections the report settled are reported after it.
		s.flush()
	case brain.ReportRepeated:
		item.Outcome = ControlAccepted
	case brain.ReportNotStarted:
		// The table may have started the job, or ended it, before its worker
		// began.
		switch a.jobState(id) {
		case job.StateQueued:
			item.Queued, item.Reason = true, reportNotStarted
		case job.StateRunning:
			item.Reason = reportStarting
		default:
			return JobControl{ID: id, Outcome: ControlFinished}
		}
	case brain.ReportNoInput:
		item.Reason = reportNoInput
		a.emitReport(id, s, kept, false)
	case brain.ReportFinishing:
		item.Reason = reportFinishing
	case brain.ReportFinished:
		return JobControl{ID: id, Outcome: ControlFinished}
	}
	return item
}

// emitReport publishes a report's acceptance for a job of the current
// conversation, with the scope that admitted it. answer says whether the
// worker will answer it.
func (a *Agency) emitReport(id string, s *jobSteer, checkpoint string, answer bool) {
	if a.pipeline == nil || !a.inAdmittingConversation(s) {
		return
	}
	a.emitJobTrace(id, events.JobReport{Scope: s.scope, ID: id, Checkpoint: checkpoint, Answer: answer})
}

// reporting reports a job asked to stop using tools and answer.
func (s *jobSteer) reporting() bool {
	if s == nil {
		return false
	}
	_, ok := s.control.Reported()
	return ok
}

// reportView is how job id's report reads in /workers, and the checkpoint it
// kept, for a job in state.
func (a *Agency) reportView(id string, state job.State) (label, checkpoint string) {
	s := a.steerFor(id)
	if s == nil {
		return "", ""
	}
	checkpoint, ok := s.control.Reported()
	if !ok {
		return "", ""
	}
	switch state {
	case job.StateRunning:
		return reportRunning, checkpoint
	case job.StateDone:
		a.mu.Lock()
		incomplete := a.workerOrigins[id].incomplete
		a.mu.Unlock()
		if incomplete == nil {
			return reportEarly, checkpoint
		}
	}
	return reportAsked, checkpoint
}

// ReportWork asks one job, from the keyboard, what it has done so far and to
// stop using tools and answer with it. It never waits for the worker.
func (p *Pipeline) ReportWork(id string) WorkReceipt {
	if p == nil || p.Agency == nil {
		return WorkReceipt{Rejected: "background work is unavailable"}
	}
	return p.Agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkReport, JobID: id})
}
