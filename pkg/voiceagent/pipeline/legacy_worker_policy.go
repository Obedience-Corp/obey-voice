package pipeline

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func legacyWorkerBudget(enabled bool, cfg *config.Config) *brain.WorkerBudget {
	if !enabled || cfg == nil || cfg.VoiceWorkers == nil || cfg.VoiceWorkers.Policy.ChargePolicy != "strict" {
		return nil
	}
	// Legacy workers retain their existing retry behavior; strict policy only
	// supplies the tool fence and does not introduce a new retry ceiling.
	return brain.NewWorkerBudget(brain.BudgetLimits{StrictTools: true, MaxRetries: int(^uint(0) >> 1)})
}
