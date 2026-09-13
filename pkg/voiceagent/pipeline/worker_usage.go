package pipeline

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func (a *Agency) workerUsageHook(id string, budget *brain.WorkerBudget) func(brain.WorkerUsage) {
	return func(u brain.WorkerUsage) {
		if a.pipeline == nil {
			return
		}
		_, key, accepted := a.callbackKey(id)
		if !accepted {
			return
		}
		var requests, tools, retries *int
		if budget != nil {
			s := budget.Snapshot()
			requests, tools, retries = &s.Requests, &s.ToolCalls, &s.Retries
		}
		a.pipeline.emit(events.WorkerUsageRecorded{Scope: key.Scope, JobID: id, Requests: requests, ToolCalls: tools, Retries: retries, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ChargeMicroUSD: u.ChargeMicroUSD})
	}
}
