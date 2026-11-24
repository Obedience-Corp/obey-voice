package pipeline

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"

// sameOutputRun is how many tool rounds in a row a job's tools have returned
// the same output, while that is worth showing. ended marks a job whose end
// has been handled, so a worker that returns late cannot show it again.
type sameOutputRun struct {
	rounds int
	ended  bool
}

// noteSameOutput records job id's run of identical tool output; 0 clears it.
// It changes nothing about the job.
func (a *Agency) noteSameOutput(id string, rounds int) {
	if id == "" {
		return
	}
	_, key, accepted := a.callbackKey(id)
	if !accepted {
		return
	}
	a.mu.Lock()
	run := a.sameOutputs[id]
	if run.ended || run.rounds == rounds {
		a.mu.Unlock()
		return
	}
	if a.sameOutputs == nil {
		a.sameOutputs = make(map[string]sameOutputRun)
	}
	a.sameOutputs[id] = sameOutputRun{rounds: rounds}
	a.mu.Unlock()
	if a.pipeline != nil {
		a.pipeline.emit(events.WorkerSameOutput{Scope: key.Scope, JobID: id, Rounds: rounds})
	}
}

// endSameOutputLocked clears job id's run for good. a.mu must be held.
func (a *Agency) endSameOutputLocked(id string) {
	if a.sameOutputs == nil {
		a.sameOutputs = make(map[string]sameOutputRun)
	}
	a.sameOutputs[id] = sameOutputRun{ended: true}
}

// sameOutputLocked is job id's current run. a.mu must be held.
func (a *Agency) sameOutputLocked(id string) int {
	return a.sameOutputs[id].rounds
}
