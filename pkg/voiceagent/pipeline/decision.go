package pipeline

import (
	"context"
	"reflect"
	"slices"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// decisionTurn holds only host-owned admission fences. Neither the provider nor
// its asynchronous goroutine can read or mutate a live brain or worker policy.
type decisionTurn struct {
	attempt *decision.Attempt
	brain   brain.Provider
	history []brain.Turn
	scope   work.WorkScope
	workers *config.VoiceWorkers
	input   string
	point   decision.Point
}

func (d *decisionTurn) close() {
	if d != nil {
		d.attempt.Close()
	}
}

func (p *Pipeline) startWorkDecision(ctx context.Context, input string, point decision.Point, skip bool) *decisionTurn {
	if skip || !p.Decisions.Enabled(point) || p.Brain == nil || p.Agency == nil || p.ConversationRetired() {
		return nil
	}
	routeCtx := p.routeContext()
	// Existing jobs and narration have followup semantics that the experimental
	// work policy does not model. Deterministic controls keep priority.
	if !routeCtx.TieredWorkers || routeCtx.JobActive || routeCtx.NarrationActive {
		return nil
	}
	if kind, _, ok := classifyTurn(input, routeCtx); ok && routeKindSkipsBrain(kind) {
		return nil
	}
	if !reflect.ValueOf(p.Brain).Comparable() {
		return nil
	}
	availability := p.workerTierAvailability()
	if availability == nil {
		return nil
	}
	var tiers []decision.Tier
	if availability.Simple {
		tiers = append(tiers, decision.Simple)
	}
	if availability.Medium {
		tiers = append(tiers, decision.Medium)
	}
	if availability.Complex {
		tiers = append(tiers, decision.Complex)
	}
	history := slices.Clone(p.Brain.History())
	// Do not truncate conversation constraints just to obtain a shortcut.
	if len(history) > 8 {
		return nil
	}
	messages := make([]decision.Message, len(history))
	for i, turn := range history {
		messages[i] = decision.Message{Role: turn.Role, Text: turn.Content}
	}
	attempt := p.Decisions.Start(ctx, point, decision.WorkInput{Text: input, Conversation: messages, EnabledTiers: tiers})
	if attempt == nil {
		return nil
	}
	return &decisionTurn{attempt: attempt, brain: p.Brain, history: history, scope: p.ConversationScope(), workers: p.Agency.WorkerSettings(), input: input, point: point}
}

// takeWorkDecision has no wait, retry or fallback call. The caller proceeds to
// its existing ThinkStream path if this single opportunity cannot be used.
func (p *Pipeline) takeWorkDecision(ctx context.Context, pending *decisionTurn, admitted string, metrics *turnMetrics) bool {
	if pending == nil {
		return false
	}
	defer pending.close()
	observation := events.DecisionEvaluated{Point: string(pending.point), Disposition: "stale"}
	defer func() { p.emit(observation) }()
	if ctx.Err() != nil || p.ConversationRetired() || pending.scope != p.ConversationScope() ||
		pending.brain != p.Brain || pending.input != admitted || p.Agency == nil ||
		!reflect.DeepEqual(pending.workers, p.Agency.WorkerSettings()) ||
		!reflect.DeepEqual(pending.history, p.Brain.History()) {
		return false
	}
	routeCtx := p.routeContext()
	if !routeCtx.TieredWorkers || routeCtx.JobActive || routeCtx.NarrationActive {
		return false
	}
	result, ready := pending.attempt.Take()
	observation.Disposition = "not_ready"
	if !ready {
		return false
	}
	observation.Model, observation.Elapsed = result.Proposal.Evidence.Model, result.Elapsed
	observation.Disposition = "unavailable"
	if result.Err != nil {
		return false
	}
	observation.Disposition = result.Proposal.Reason
	if !result.Proposal.Accepted {
		return false
	}
	observation.Disposition, observation.Tier = "proposed", string(result.Proposal.Tier)
	metrics.route = RouteDecision{Kind: RouteSpawn, Tier: config.WorkerTierName(result.Proposal.Tier), ReasonCode: result.Proposal.ReasonCode, Source: "decision"}
	return true
}
