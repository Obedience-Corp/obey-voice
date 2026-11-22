package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

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

// workerFacts is what a live request is told about existing jobs. A turn
// answering a person describes every running and queued one, for the renderer
// to list or count; the tiered work record fills what room is left with its
// own jobs by identifier on every turn.
func (p *Pipeline) workerFacts(answersPerson bool) []brain.WorkerFact {
	if p.Agency == nil {
		return nil
	}
	var facts []brain.WorkerFact
	if answersPerson {
		facts = p.Agency.activeJobFacts()
	}
	if tieredWorkers(p.Agency.currentConfig()) {
		described := make(map[string]bool, len(facts))
		for _, fact := range facts {
			described[fact.ID] = true
		}
		for _, v := range p.Agency.WorkSnapshot().Jobs {
			if len(facts) >= workerFactLimit {
				break
			}
			if !described[v.Key.JobID] {
				facts = append(facts, brain.WorkerFact{ID: v.Key.JobID, Tier: config.WorkerTierName(v.Config["worker_tier"]), State: string(v.State)})
			}
		}
	}
	return facts
}

// workerFactLimit is how many jobs the tiered work record contributes to.
const workerFactLimit = 8

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
	if a.jobs != nil && next.Mode == config.WorkerModeTiered {
		a.jobs.SetWorkerLimits(next.Policy.ConcurrentWorkers, next.Policy.QueuedWorkers)
	}
	return nil
}

// admitWorker resolves only configuration here. Catalog I/O and provider
// construction happen on the job goroutine, without coordinator/config locks.
func (a *Agency) admitWorker(tier config.WorkerTierName) (brain.Provider, *config.Config, *config.WorkerPlan, error) {
	return a.admitWorkerFrom(a.workerSource(), tier)
}

// workerSource is the worker configuration a spawn is admitted from.
type workerSource struct {
	worker brain.Provider
	cfg    *config.Config
	err    error
}

func (a *Agency) workerSource() workerSource {
	worker, cfg, err := a.workerAdmission()
	return workerSource{worker: worker, cfg: cfg, err: err}
}

func (a *Agency) admitWorkerFrom(source workerSource, tier config.WorkerTierName) (brain.Provider, *config.Config, *config.WorkerPlan, error) {
	worker, cfg, legacyErr := source.worker, cloneConfig(source.cfg), source.err
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
	requestID := newWorkID()
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		if plan.Assignment.Billing != config.WorkerBillingLocal {
			if err := a.recheckWorkerPolicy(plan); err != nil {
				return "", err
			}
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(plan.Limits.RuntimeSeconds)*time.Second)
		defer cancel()
		limits := brain.BudgetLimits{StrictTools: plan.Policy.ChargePolicy == "strict", MaxRequests: plan.Limits.ModelRequests, MaxToolCalls: plan.Limits.ToolCalls, MaxRetries: plan.Limits.TransportRetries, MaxRuntime: time.Duration(plan.Limits.RuntimeSeconds) * time.Second}
		if plan.Limits.OutputTokens != nil {
			limits.MaxOutputTokens = *plan.Limits.OutputTokens
		}
		budget := brain.NewWorkerBudget(limits)
		budget.BeforeRequest = func() error { return a.recheckWorkerPolicy(plan) }
		ctx = budget.Context(ctx)
		factory := a.planFactory
		worker, err := factory(ctx, cfg.Clone(), plan)
		if err != nil {
			return "", fmt.Errorf("prepare worker: %w", err)
		}
		if plan.Assignment.Billing != config.WorkerBillingLocal {
			return a.paidWorkerRun(ctx, brief, worker, cfg, plan, requestID)
		}
		return a.workerRunWithBudget(brief, worker, cfg, budget, true)(ctx)
	}
}

// Rechecked immediately before every provider request; pinned assignments never
// grant permission to ignore a later installation-wide paid disable.
func (a *Agency) recheckWorkerPolicy(plan *config.WorkerPlan) error {
	if plan.Assignment.Billing == config.WorkerBillingLocal {
		return nil
	}
	current := a.WorkerSettings()
	if current == nil || !current.Policy.PaidEnabled {
		return fmt.Errorf("%w: paid workers are disabled", ErrWorkerPolicyChanged)
	}
	return nil
}
