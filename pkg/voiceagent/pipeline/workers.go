package pipeline

import (
	"errors"
	"sort"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// WorkerInfo is a read-only projection for worker controls. Ownership and
// cancellation remain in the original runtime, even after persona replacement.
type WorkerInfo struct {
	ID, Task, State, Provider, Model   string
	Activity, Result, Err, Termination string
	Started, Ended                     time.Time
	Elapsed                            time.Duration
	Tools                              []workfold.Call
	Thread                             []brain.Turn
	PersonaID, PersonaName             string
	// Correction is how the job's newest correction stands, if it has one.
	Correction string
	// Report is how a report asked of the job stands, and Checkpoint what the
	// job had done when it was asked.
	Report, Checkpoint string
	// SameOutput is how many tool rounds in a row a running job's tools have
	// returned the same output, while that is worth showing.
	SameOutput int
}

// abortedState is how an aborted table job shows: stopping until its worker
// is seen to return.
func abortedState(item job.Info) (state, termination string) {
	switch {
	case item.Stopping:
		return "stopping", "unknown"
	case item.Started.IsZero():
		return string(item.State), "confirmed (never started)"
	default:
		return string(item.State), "confirmed (worker returned)"
	}
}

func (i WorkerInfo) Active() bool {
	return i.State == "queued" || i.State == "running" || i.State == "stopping"
}

func managedRuntime(provider brain.Provider) *delegation.Runtime {
	if owner, ok := provider.(interface{ ManagedRuntime() *delegation.Runtime }); ok {
		return owner.ManagedRuntime()
	}
	return nil
}

func (a *Agency) bindWorkerAdmission(provider brain.Provider) {
	if runtime := managedRuntime(provider); runtime != nil {
		runtime.SetAdmissionCheck(a.jobs.CheckAdmission)
		a.guardToolCancels(runtime)
	}
}

func (a *Agency) bindConversationWorkers(provider brain.Provider) {
	a.bindWorkerAdmission(provider)
	var getter func() *delegation.Runtime
	if owner, ok := provider.(interface{ ManagedRuntime() *delegation.Runtime }); ok {
		getter = owner.ManagedRuntime
	}
	a.mu.Lock()
	a.liveManaged = getter
	a.mu.Unlock()
}

func (a *Agency) currentManagedRuntime() *delegation.Runtime {
	a.mu.Lock()
	getter := a.liveManaged
	a.mu.Unlock()
	if getter != nil {
		return getter()
	}
	return nil
}

// SetWorkersEnabled affects new admissions only, not existing queued jobs.
func (p *Pipeline) SetWorkersEnabled(enabled bool) {
	if p != nil && p.Agency != nil {
		p.Agency.jobs.SetEnabled(enabled)
	}
}

func (p *Pipeline) WorkersEnabled() bool {
	return p != nil && p.Agency != nil && p.Agency.jobs.CheckAdmission() == nil
}

// Workers returns cached state only. It never waits on a provider or network.
// Canonical outcomes take precedence over legacy runtime projections.
func (p *Pipeline) Workers() []WorkerInfo {
	if p == nil || p.Agency == nil {
		return nil
	}
	a := p.Agency
	byID := make(map[string]WorkerInfo)
	canonical := a.WorkSnapshot()
	table := make(map[string]job.Info)
	for _, item := range a.jobs.Status() {
		table[item.ID] = item
		// A clear/restore retires old table IDs; don't leak them into this chat.
		if canonical.Scope.Valid() {
			a.workMu.RLock()
			scope := a.jobScopes[item.ID]
			a.workMu.RUnlock()
			if scope != canonical.Scope {
				continue
			}
		}
		info := WorkerInfo{ID: item.ID, Task: item.Task, State: string(item.State),
			Provider: item.Provider, Model: item.Model, Result: item.Result, Err: item.Err,
			Started: item.Started, Ended: item.Ended, Elapsed: item.Elapsed()}
		if a.work != nil {
			info.Tools = a.work.Calls(item.ID)
			if rows := a.work.Rows(item.ID); len(rows) > 0 {
				latest := rows[len(rows)-1]
				info.Activity = latest.String()
				if latest.Last != "" {
					info.Activity += " · " + latest.Last
				}
			}
		}
		if item.State == job.StateAborted {
			info.State, info.Termination = abortedState(item)
		}
		info.Correction = a.steerFor(item.ID).label()
		info.Report, info.Checkpoint = a.reportView(item.ID, item.State)
		a.mu.Lock()
		info.Thread = copyWorkerThread(a.workerThreads[item.ID])
		origin := a.workerOrigins[item.ID]
		info.PersonaID, info.PersonaName = origin.Envelope.PersonaID, origin.Envelope.DisplayName
		same := a.sameOutputLocked(item.ID)
		a.mu.Unlock()
		if origin.Scope.Valid() && (origin.Scope != p.ConversationScope() || p.ConversationRetired()) {
			continue
		}
		if item.State == job.StateRunning {
			info.SameOutput = same
		}
		byID[item.ID] = info
	}
	a.workMu.RLock()
	runtimes := make(map[*delegation.Runtime]struct{}, len(a.managed))
	for runtime := range a.managed {
		runtimes[runtime] = struct{}{}
	}
	a.workMu.RUnlock()
	if runtime := a.currentManagedRuntime(); runtime != nil {
		runtimes[runtime] = struct{}{}
	}
	for runtime := range runtimes {
		for _, task := range runtime.Tasks() {
			elapsed := task.Elapsed
			if task.Ended.IsZero() && !task.Started.IsZero() {
				elapsed = time.Since(task.Started)
			}
			byID[task.Ticket] = WorkerInfo{ID: task.Ticket, Task: task.Task, State: task.State,
				Provider: "obey", Result: task.Brief, Err: task.Err, Termination: task.Termination,
				Started: task.Started, Ended: task.Ended, Elapsed: elapsed}
		}
	}
	for _, item := range canonical.Jobs {
		info := byID[item.Key.JobID]
		info.ID, info.Task, info.Provider, info.Model = item.Key.JobID, item.Task, item.Provider, item.Model
		// Terminal jobs no longer have a live callback scope, but their
		// messages still belong to this conversation's canonical job.
		a.mu.Lock()
		info.Thread = copyWorkerThread(a.workerThreads[info.ID])
		origin := a.workerOrigins[info.ID]
		info.PersonaID, info.PersonaName = origin.Envelope.PersonaID, origin.Envelope.DisplayName
		same := a.sameOutputLocked(info.ID)
		a.mu.Unlock()
		if info.State != "stopping" {
			info.State = string(item.State)
		}
		info.Correction = a.steerFor(info.ID).label()
		info.Report, info.Checkpoint = a.reportView(info.ID, table[info.ID].State)
		if item.Model == "" && item.Origin == "managed" {
			info.Model = item.Config["profile"]
		}
		if out := item.Outcome; out != nil {
			info.State, info.Result, info.Err, info.Termination = string(out.Status), out.Text, out.Limitations, out.Termination
			info.Started, info.Ended, info.Elapsed = out.StartedAt, out.EndedAt, out.Elapsed
		}
		// The outcome is published when the job ends; whether its worker has
		// returned since is only known to the table.
		if local, ok := table[info.ID]; ok && local.State == job.StateAborted {
			state, termination := abortedState(local)
			if state == "stopping" {
				info.State = state
			}
			if info.Termination == "" || info.Termination == "unknown" {
				info.Termination = termination
			}
		}
		info.SameOutput = 0
		if info.State == string(work.JobRunning) {
			info.SameOutput = same
		}
		byID[info.ID] = info
	}
	result := make([]WorkerInfo, 0, len(byID))
	for _, info := range byID {
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Active() != result[j].Active() {
			return result[i].Active()
		}
		if !result[i].Started.Equal(result[j].Started) {
			return result[i].Started.After(result[j].Started)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// StopWorkers requests cancellation from every current owner, including
// managed jobs retained from previous personas. It does not disable admission.
func (p *Pipeline) StopWorkers() error {
	if p == nil || p.Agency == nil {
		return nil
	}
	receipt := p.Agency.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkCancel, All: true})
	if err := receipt.Err(); err != nil && !errors.Is(err, work.ErrUnknown) {
		return err
	}
	return nil
}
