package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// startBrainStream gives harnesses the routing boundary that Ollama implements
// in its tool loop. Classification never enters the live conversation/session.
func (p *Pipeline) startBrainStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	opts.InputBindingRevision = p.inputBindingRevision.Load()
	if voices, ok := ctx.Value(responseVoicesKey{}).(responseVoices); ok {
		opts.InputBindingRevision = voices.revision
	}
	classifier, ok := p.Brain.(brain.LiveRouteClassifier)
	if !ok || p.Agency == nil || !opts.ToolsEnabled || opts.RouterToolsPolicy != brain.RouterToolsRouteOnly ||
		opts.RetryInput || opts.AgentContinuation || opts.Background || opts.Discussion != nil || opts.OmitTurnInstruction ||
		(opts.Utterance != nil && p.Native != nil && p.Native.Controls == "conversation") {
		return p.Brain.ThinkStream(ctx, input, opts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.ConversationRetired() {
		return nil, fmt.Errorf("conversation retired before harness routing")
	}
	scope := p.ConversationScope()
	responder := p.Brain
	history := p.Brain.History()
	if projected, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		history = projected.ContextHistory()
	}
	started := time.Now()
	result, err := classifier.ClassifyLiveRoute(ctx, brain.LiveRouteRequest{Text: input, Conversation: history, WorkerTiers: opts.WorkerTiers})
	contextLimited := errors.Is(err, brain.ErrHarnessRouteContextLimit)
	disposition := "chat"
	if result.Route != "" {
		disposition = result.Route
	}
	if err != nil {
		disposition = "failed"
	}
	if contextLimited {
		disposition = "context_limit"
		result = brain.StreamResult{}
	}
	p.emit(events.DecisionEvaluated{Point: "harness_route", Disposition: disposition, Elapsed: time.Since(started), Tier: string(result.RouteTier)})
	if err != nil && !contextLimited {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.ConversationRetired() || p.ConversationScope() != scope || p.Brain != responder {
		return nil, fmt.Errorf("conversation changed during harness routing")
	}
	if result.Route != "" {
		chunks := make(chan string)
		close(chunks)
		done := make(chan brain.StreamResult, 1)
		done <- result
		close(done)
		return &brain.Stream{Chunks: chunks, Done: done}, nil
	}
	cfg := p.Agency.currentConfig()
	available := cfg != nil && !cfg.VoiceWorkersDisabled
	// A persona doing its own work needs no configured worker or tier.
	if _, own := p.Agency.ownWork(); !own {
		if tiers := opts.WorkerTiers; tiers != nil {
			available = available && (tiers.Simple || tiers.Medium || tiers.Complex)
		} else {
			_, workerErr := p.Agency.workerProvider()
			available = available && workerErr == nil
		}
	}
	opts.HarnessRouting = &brain.HarnessRouting{WorkersAvailable: available, ContextLimited: contextLimited}
	return p.Brain.ThinkStream(ctx, input, opts)
}
