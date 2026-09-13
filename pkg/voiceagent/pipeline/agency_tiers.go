package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

var ErrWorkerPolicyChanged = errors.New("worker policy changed")

func (p *Pipeline) workerTierAvailability() *brain.WorkerTierAvailability {
	if p.Agency == nil {
		return nil
	}
	return brain.WorkerTierAvailabilityForConfig(p.Agency.currentConfig())
}

func (p *Pipeline) workerFacts() []brain.WorkerFact {
	if p.Agency == nil || !tieredWorkers(p.Agency.currentConfig()) {
		return nil
	}
	var facts []brain.WorkerFact
	for _, v := range p.Agency.WorkSnapshot().Jobs {
		facts = append(facts, brain.WorkerFact{ID: v.Key.JobID, Tier: config.WorkerTierName(v.Config["worker_tier"]), State: string(v.State)})
		if len(facts) == 8 {
			break
		}
	}
	return facts
}

func tieredWorkers(cfg *config.Config) bool {
	return cfg != nil && cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered
}

// WorkerSettings returns an independent installation-policy snapshot. It does
// not include the selected persona's legacy worker override.
func (a *Agency) WorkerSettings() *config.VoiceWorkers {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return nil
	}
	return a.cfg.VoiceWorkers.Clone()
}

// WorkerMigrationConfig captures the effective singleton worker, including the
// selected persona's overrides, for an explicit Settings migration preview.
func (a *Agency) WorkerMigrationConfig() *config.Config { return a.currentConfig() }

// ApplyWorkerSettings publishes a successfully committed Settings revision for
// future admissions. Existing job closures retain their original plan.
func (a *Agency) ApplyWorkerSettings(settings *config.VoiceWorkers) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	next := settings.Clone()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return errors.New("worker configuration is unavailable")
	}
	if old := a.cfg.VoiceWorkers; old != nil {
		if next.Revision < old.Revision || (next.Revision == old.Revision && !reflect.DeepEqual(next, old)) {
			return config.ErrWorkerRevisionConflict
		}
	}
	a.cfg.VoiceWorkers = next
	return nil
}

// admitWorker resolves only configuration here. Catalog I/O and provider
// construction happen on the job goroutine, without coordinator/config locks.
func (a *Agency) admitWorker(tier config.WorkerTierName) (brain.Provider, *config.Config, *config.WorkerPlan, error) {
	worker, cfg, legacyErr := a.workerAdmission()
	if cfg != nil && cfg.VoiceWorkers != nil {
		if err := cfg.VoiceWorkers.Validate(); err != nil {
			return nil, cfg, nil, err
		}
	}
	if !tieredWorkers(cfg) {
		return worker, cfg, nil, legacyErr
	}
	plan, err := config.ResolveWorkerPlan(cfg, tier, brain.LocalWorkerCapabilities{})
	if err != nil {
		return nil, cfg, nil, err
	}
	// Keep legacy accessors and persisted provider/model labels consistent with
	// the plan while retaining the persona and tool configuration at admission.
	cfg.VoiceWorkerProvider = plan.Assignment.Provider
	cfg.VoiceWorkerModel = plan.Assignment.Model
	if plan.Assignment.EndpointRef != nil {
		cfg.VoiceWorkerOllamaHost = *plan.Assignment.EndpointRef
	}
	return nil, cfg, plan, nil
}

func frozenWorkerConfig(cfg *config.Config, plan *config.WorkerPlan) map[string]string {
	values := frozenConfig(cfg)
	if plan != nil {
		// A tiered worker is admitted with its complete tool surface even when
		// live conversation tools are disabled. Keep persisted provenance
		// truthful about the options used by plannedWorkerRun.
		values["tools"] = "true"
		encoded, _ := json.Marshal(plan) // plan contains only schema scalar values
		values["worker_plan"] = string(encoded)
		values["worker_tier"] = string(plan.Tier)
		values["worker_revision"] = fmt.Sprint(plan.ConfigRevision)
	}
	return values
}

func (a *Agency) plannedWorkerRun(brief string, cfg *config.Config, plan *config.WorkerPlan) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		// Paid/unknown work is disabled until the usage-policy task supplies
		// enforcing adapters. A saved billing label cannot bypass validation.
		if plan.Assignment.Billing != config.WorkerBillingLocal {
			current := a.WorkerSettings()
			if current == nil || !current.Policy.PaidEnabled {
				return "", fmt.Errorf("%w: paid workers are disabled", ErrWorkerPolicyChanged)
			}
			return "", errors.New("paid worker dispatch is unavailable until usage policy is enforced")
		}
		factory := a.planFactory
		worker, err := factory(ctx, cfg.Clone(), plan)
		if err != nil {
			return "", fmt.Errorf("prepare worker: %w", err)
		}
		return a.workerRun(brief, worker, cfg)(ctx)
	}
}
