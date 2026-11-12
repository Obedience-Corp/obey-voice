package pipeline

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// ownWork is the job configuration when the bound persona does its own
// background work, or false when the configured worker does it. It is the one
// place that choice is made.
func (a *Agency) ownWork() (*config.Config, bool) {
	cfg := a.currentConfig()
	if cfg == nil || config.WorkOf(cfg) != config.WorkOwn {
		return nil, false
	}
	own := config.OwnWorkConfig(cfg)
	// The experimental inbox runs local workers only; a persona on a hosted
	// backend hands its work to that worker, as before.
	if a.workEnabled() && providerName(own) != "ollama" {
		return nil, false
	}
	return own, true
}

// ownWorkRun builds the persona's own backend as a worker on the job's
// goroutine: a provider of its own, separate from the one the persona talks
// on, so the talking session is never resumed or filled with the job's tool
// traffic. It then runs like any legacy worker.
func (a *Agency) ownWorkRun(brief string, cfg *config.Config) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		factory := a.ownFactory
		if factory == nil {
			factory = newOwnWorker
		}
		worker, err := factory(ctx, cfg)
		if err != nil {
			return "", fmt.Errorf("prepare %s for its own work: %w", providerName(cfg), err)
		}
		defer func() { _ = brain.CloseProvider(worker) }()
		return a.workerRun(brief, worker, cfg)(ctx)
	}
}

func newOwnWorker(_ context.Context, cfg *config.Config) (brain.Provider, error) {
	return brain.NewWorkerProvider(cfg)
}
