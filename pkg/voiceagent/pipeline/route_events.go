package pipeline

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func (a *Agency) routeDecisionEvent(d RouteDecision, state, id, message string) events.RouteDecided {
	tier := string(d.Tier)
	if !tieredWorkers(a.currentConfig()) {
		tier = "legacy"
	}
	return events.RouteDecided{Scope: a.pipeline.ConversationScope(), Kind: string(d.Kind), Tier: tier, ReasonCode: d.ReasonCode, State: state, JobID: id, Task: strings.TrimSpace(d.Transcript), Message: message}
}

func (a *Agency) emitRouteDecision(d RouteDecision, state, id, message string) {
	a.pipeline.emit(a.routeDecisionEvent(d, state, id, message))
}
