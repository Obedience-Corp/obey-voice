package pipeline

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// StartWarmup preloads the conversation's model in the background, so the
// first turn is not the one that pays for loading it. A brain that can be told
// the turn's routing gets the options an ordinary turn sends. The host calls
// this once the brain holds the state the first response will see.
func (p *Pipeline) StartWarmup(ctx context.Context) {
	switch b := p.Brain.(type) {
	case brain.TurnWarmer:
		opts := brain.StreamOptions{
			WorkerTiers:       p.workerTierAvailability(),
			ToolsEnabled:      p.liveToolsEnabled(),
			RouterToolsPolicy: p.liveRouterToolsPolicy(),
			RouteJSON:         p.liveRouteJSON(),
			BackgroundWorker:  p.liveRouterToolsPolicy() == brain.RouterToolsDirect && p.backgroundWorkerAvailable(),
		}
		go b.WarmupTurn(ctx, opts)
	case brain.Warmer:
		go b.Warmup(ctx)
	}
}
