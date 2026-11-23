package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// WorkActorKind says who asked for a control on background work. The host
// fills it in; nothing a model writes can choose it.
type WorkActorKind string

const (
	// WorkActorCommand is a keyboard, TUI or desktop command.
	WorkActorCommand WorkActorKind = "command"
	// WorkActorPhrase is one of the exact spoken cancel phrases.
	WorkActorPhrase WorkActorKind = "phrase"
	// WorkActorTool is the model-facing agent_cancel tool.
	WorkActorTool WorkActorKind = "tool"
	// WorkActorHost is the host acting on its own.
	WorkActorHost WorkActorKind = "host"
)

// WorkActor is who asked. Runtime is set for a tool actor: the managed
// runtime whose tool made the call.
type WorkActor struct {
	Kind    WorkActorKind
	Runtime *delegation.Runtime
}

// WorkAction is what a control does to a job.
type WorkAction string

const (
	// WorkCancel ends a job now.
	WorkCancel WorkAction = "cancel"
	// WorkSteer gives a job a correction, which it reads at its next model
	// request or tool call, or in its brief if it has not started.
	WorkSteer WorkAction = "steer"
)

// WorkRequest names the action and the job, or every job with All.
// Instruction is a correction's text.
type WorkRequest struct {
	Action      WorkAction
	JobID       string
	All         bool
	Instruction string
}

// ControlOutcome is what a control found and did to one job.
type ControlOutcome string

const (
	// ControlStopped: the job's run function has returned, or it never ran.
	ControlStopped ControlOutcome = "stopped"
	// ControlStopping: the job is cancelled; its run function has not returned.
	ControlStopping ControlOutcome = "stopping"
	// ControlRequested: a managed task was asked to cancel; its owner reports
	// when it ends.
	ControlRequested ControlOutcome = "requested"
	// ControlFinished: the job had already ended.
	ControlFinished ControlOutcome = "already_finished"
	// ControlUnknown: no such job in this conversation.
	ControlUnknown ControlOutcome = "unknown"
	// ControlRejected: the actor may not control this job, or the owner refused.
	ControlRejected ControlOutcome = "rejected"
	// ControlAccepted: a correction waits for the running worker's next
	// model request or tool call.
	ControlAccepted ControlOutcome = "accepted"
	// ControlBriefed: the job had not started; the correction was added to
	// the brief its worker will read.
	ControlBriefed ControlOutcome = "brief_updated"
)

// JobControl is the receipt for one job.
type JobControl struct {
	ID      string
	Outcome ControlOutcome
	// Managed marks a task owned by an Obey-managed delegation runtime.
	Managed bool
	// Queued means the cancel found the job waiting to start.
	Queued bool
	// Ran is how long the job had run when the cancel landed.
	Ran    time.Duration
	Reason string
	// Revision numbers an accepted correction within its job.
	Revision int
}

// WorkReceipt says what a control was accepted for and what has been
// observed of each job it touched.
type WorkReceipt struct {
	Actor   WorkActor
	Request WorkRequest
	Jobs    []JobControl
	// Rejected is why the whole request was refused, if it was.
	Rejected string
}

// errControlRejected is wrapped by Err for a refused control.
var errControlRejected = errors.New("work control rejected")

// Err is the receipt as an error for callers that only report failure: nil
// when the control was applied or the job had already ended.
func (r WorkReceipt) Err() error {
	if r.Rejected != "" {
		return fmt.Errorf("%w: %s", errControlRejected, r.Rejected)
	}
	var errs []error
	for _, item := range r.Jobs {
		switch item.Outcome {
		case ControlUnknown:
			errs = append(errs, work.ErrUnknown)
		case ControlRejected:
			errs = append(errs, errors.New(item.Reason))
		}
	}
	return errors.Join(errs...)
}

// Stopping lists the jobs whose run function had not returned when the
// receipt was taken.
func (r WorkReceipt) Stopping() []string {
	var ids []string
	for _, item := range r.Jobs {
		if item.Outcome == ControlStopping {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// cancelExitWait is how long the cancelling turn waits for cancelled workers
// to return before choosing its line. It only chooses the wording; the job
// itself is already over. It covers a CLI worker's interrupt and process
// reaping, about a second.
const cancelExitWait = 1500 * time.Millisecond

// unknownTask is the one refusal a tool gets for any ticket that is not its
// own, so it learns nothing about work it may not touch.
const unknownTask = "unknown background task"

// ControlWork is the one door for interactive controls on background work.
// Lifecycle stops (session reset, budget, shutdown, a moved reply that could
// not be taken over) stay internal and do not come through here. It never
// waits for a worker to exit.
func (a *Agency) ControlWork(actor WorkActor, req WorkRequest) WorkReceipt {
	receipt := WorkReceipt{Actor: actor, Request: req}
	switch {
	case req.Action == WorkSteer:
		a.steerJob(&receipt)
	case req.Action != WorkCancel:
		receipt.Rejected = fmt.Sprintf("unsupported work action %q", req.Action)
	case !req.All && req.JobID == "":
		receipt.Rejected = "no job named"
	case actor.Kind == WorkActorTool:
		a.toolCancel(&receipt)
	case actor.Kind == WorkActorCommand, actor.Kind == WorkActorPhrase, actor.Kind == WorkActorHost:
		if req.All {
			a.cancelAll(&receipt)
		} else {
			receipt.Jobs = append(receipt.Jobs, a.cancelOne(req.JobID))
		}
	default:
		receipt.Rejected = fmt.Sprintf("unknown actor %q", actor.Kind)
	}
	return receipt
}

// toolCancel lets agent_cancel reach only its own runtime's live task.
func (a *Agency) toolCancel(receipt *WorkReceipt) {
	runtime, id := receipt.Actor.Runtime, receipt.Request.JobID
	if receipt.Request.All || runtime == nil {
		receipt.Rejected = "a tool may cancel only one of its own tasks"
		return
	}
	item := JobControl{ID: id, Managed: true, Outcome: ControlRejected, Reason: unknownTask}
	for _, task := range runtime.Tasks() {
		if task.Ticket != id && task.SourceRef != id {
			continue
		}
		if !task.Ended.IsZero() {
			item.Outcome, item.Reason = ControlFinished, ""
			break
		}
		item = a.cancelManaged(runtime, id)
		break
	}
	receipt.Jobs = append(receipt.Jobs, item)
}

func (a *Agency) cancelManaged(runtime *delegation.Runtime, id string) JobControl {
	item := JobControl{ID: id, Managed: true, Outcome: ControlRequested}
	if err := runtime.Cancel(id); err != nil {
		item.Outcome, item.Reason = ControlRejected, err.Error()
	}
	return item
}

// managedOwner finds the runtime that owns a live managed task, including
// one retained from an earlier persona.
func (a *Agency) managedOwner(id string) *delegation.Runtime {
	a.workMu.RLock()
	runtime := a.managedJobs[id]
	a.workMu.RUnlock()
	if runtime != nil {
		return runtime
	}
	if runtime := a.currentManagedRuntime(); runtime != nil {
		for _, task := range runtime.Tasks() {
			if task.Ticket == id && task.Ended.IsZero() {
				return runtime
			}
		}
	}
	return nil
}

func (a *Agency) cancelOne(id string) JobControl {
	if runtime := a.managedOwner(id); runtime != nil {
		return a.cancelManaged(runtime, id)
	}
	if !a.jobInConversation(id) {
		return JobControl{ID: id, Outcome: ControlUnknown}
	}
	// The table's ending of the job settles its corrections; this marks that
	// ending, if this cancel wins it, as a cancel.
	a.steerFor(id).markCancel()
	before, ok := a.jobs.Cancel(id)
	if !ok {
		return JobControl{ID: id, Outcome: ControlUnknown}
	}
	return a.tableControl(before)
}

// cancelAll ends every table job in this conversation as one act, then asks
// every live managed task to cancel.
func (a *Agency) cancelAll(receipt *WorkReceipt) {
	outside := map[string]bool{}
	for _, info := range a.jobs.Status() {
		if !a.jobInConversation(info.ID) {
			outside[info.ID] = true
		}
	}
	// The selection runs under the table lock, so the jobs marked as
	// cancelled are exactly the ones this cancel ends.
	for _, before := range a.jobs.CancelAll(func(id string) bool {
		if outside[id] {
			return true
		}
		a.steerFor(id).markCancel()
		return false
	}) {
		receipt.Jobs = append(receipt.Jobs, a.tableControl(before))
	}
	for _, runtime := range a.managedRuntimes() {
		for _, task := range runtime.Tasks() {
			if task.Ended.IsZero() && task.State != "stopping" {
				receipt.Jobs = append(receipt.Jobs, a.cancelManaged(runtime, task.Ticket))
			}
		}
	}
}

// tableControl turns the state a cancel found into its receipt entry.
func (a *Agency) tableControl(before job.Info) JobControl {
	item := JobControl{ID: before.ID, Outcome: ControlFinished}
	switch before.State {
	case job.StateQueued:
		item.Outcome, item.Queued = ControlStopped, true
	case job.StateRunning:
		item.Outcome, item.Ran = ControlStopped, before.Elapsed()
		if a.jobStopping(before.ID) {
			item.Outcome = ControlStopping
		}
	}
	return item
}

func (a *Agency) jobStopping(id string) bool {
	for _, info := range a.jobs.Status() {
		if info.ID == id {
			return info.Stopping
		}
	}
	return false
}

// awaitExits waits, briefly and only when something was still stopping, for
// cancelled workers to return, and marks the ones that did as stopped.
func (a *Agency) awaitExits(ctx context.Context, receipt WorkReceipt) WorkReceipt {
	ids := receipt.Stopping()
	if len(ids) == 0 {
		return receipt
	}
	wait, cancel := context.WithTimeout(ctx, a.cancelExitWait)
	defer cancel()
	_ = a.jobs.AwaitExit(wait, ids...)
	for i, item := range receipt.Jobs {
		if item.Outcome == ControlStopping && !a.jobStopping(item.ID) {
			receipt.Jobs[i].Outcome = ControlStopped
		}
	}
	return receipt
}

// jobInConversation reports whether a table job is one a command or a phrase
// may control: with the work inbox on, a job of the current conversation.
// Without it every table job is, because a cleared conversation there leaves
// its jobs running and holding their slots with no other way to reach them.
func (a *Agency) jobInConversation(id string) bool {
	c, scope := a.workAuthority()
	if c == nil {
		return true
	}
	a.workMu.RLock()
	jobScope, ok := a.jobScopes[id]
	a.workMu.RUnlock()
	if ok {
		return jobScope == scope
	}
	// A finished job's scope entry is dropped; the snapshot still has it.
	for _, item := range c.Snapshot(scope).Jobs {
		if item.Key.JobID == id {
			return true
		}
	}
	return false
}

// managedRuntimes are every runtime that may own a live managed task in this
// conversation: retained ones and the live brain's.
func (a *Agency) managedRuntimes() []*delegation.Runtime {
	a.workMu.RLock()
	seen := make(map[*delegation.Runtime]bool, len(a.managed)+1)
	out := make([]*delegation.Runtime, 0, len(a.managed)+1)
	for runtime := range a.managed {
		seen[runtime] = true
		out = append(out, runtime)
	}
	a.workMu.RUnlock()
	if runtime := a.currentManagedRuntime(); runtime != nil && !seen[runtime] {
		out = append(out, runtime)
	}
	return out
}

// managedActive reports whether a managed task is live and not yet asked to
// stop, which arms the spoken cancel phrases as a queued or running job does.
func (a *Agency) managedActive() bool {
	for _, runtime := range a.managedRuntimes() {
		for _, task := range runtime.Tasks() {
			if task.Ended.IsZero() && task.State != "stopping" {
				return true
			}
		}
	}
	return false
}

// guardToolCancels sends a runtime's agent_cancel through ControlWork.
func (a *Agency) guardToolCancels(runtime *delegation.Runtime) {
	runtime.SetCancelGuard(func(from *delegation.Runtime, ticket string) error {
		receipt := a.ControlWork(WorkActor{Kind: WorkActorTool, Runtime: from}, WorkRequest{Action: WorkCancel, JobID: ticket})
		if len(receipt.Jobs) == 1 && receipt.Jobs[0].Outcome == ControlFinished {
			return errors.New("background task already finished")
		}
		return receipt.Err()
	})
}
