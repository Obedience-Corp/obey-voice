package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// workDecision is who runs a turn's background work. A turn resolves it once,
// when it is admitted, so what the router is told and what the spawn does
// cannot disagree when the persona is edited or switched in between.
type workDecision struct {
	// own is the job config when the persona does its own work; nil hands
	// the work to the configured worker.
	own *config.Config
	// bound is the persona's binding, which holds the tiers in tiered mode.
	bound *config.Config
	// refused is why the persona's own work cannot run under the current
	// policy; set only with own.
	refused error
}

type turnWorkKey struct{}

// resolveWork is the one place the choice is made.
func (a *Agency) resolveWork() workDecision {
	cfg := a.currentConfig()
	if cfg == nil || config.WorkOf(cfg) != config.WorkOwn {
		return workDecision{}
	}
	own := config.OwnWorkConfig(cfg)
	provider := providerName(own)
	// The experimental inbox runs local workers only; a persona on a hosted
	// backend hands its work to that worker, as before.
	if a.workEnabled() && provider != "ollama" {
		return workDecision{}
	}
	d := workDecision{own: own, bound: cfg}
	switch {
	case config.OwnWorkStrict(cfg) && !brain.WorkerCapabilitiesFor(provider).ToolCallMediation.Supported:
		d.refused = fmt.Errorf("%s cannot do its own work under the strict worker policy: %s runs its own tools and requests, which that policy cannot restrict. Set its work to worker, or use the advisory policy",
			personaLabel(cfg), provider)
	default:
		d.refused = config.OwnWorkTiersClosed(cfg)
	}
	return d
}

// admit is the routed tier an own job runs under, or why it cannot run.
func (d workDecision) admit(tier config.WorkerTierName) (*config.OwnTier, error) {
	if d.refused != nil {
		return nil, d.refused
	}
	return config.OwnWorkTier(d.bound, tier)
}

// withTurnWork carries the admitted persona's work decision through the turn.
func (p *Pipeline) withTurnWork(ctx context.Context) context.Context {
	if p.Agency == nil {
		return ctx
	}
	return context.WithValue(ctx, turnWorkKey{}, p.Agency.resolveWork())
}

// turnWork is the decision the turn carries, or a fresh one for a route that
// did not come through an admitted turn.
func (a *Agency) turnWork(ctx context.Context) workDecision {
	if d, ok := ctx.Value(turnWorkKey{}).(workDecision); ok {
		return d
	}
	return a.resolveWork()
}

func personaLabel(cfg *config.Config) string {
	if name := strings.TrimSpace(cfg.AgentName); name != "" {
		return name
	}
	if id := strings.TrimSpace(cfg.ActivePersona); id != "" {
		return id
	}
	return "this persona"
}

// ownRuntimeBudget is how long an own job may run: voice_worker_job_budget
// when it is set, else the hosted default on a hosted backend unless the
// budget was set to unlimited, and never longer than the routed tier allows.
// Zero leaves the job table's budget in force, which a local own job keeps.
func (a *Agency) ownRuntimeBudget(d workDecision, tier *config.OwnTier) time.Duration {
	limit := a.jobs.Budget()
	if limit == 0 && brain.IsHosted(providerName(d.own)) && (d.bound == nil || d.bound.VoiceWorkerJobBudget != config.UnlimitedJobBudget) {
		limit = a.ownJobDefault
	}
	if tier != nil {
		if t := time.Duration(tier.Limits.RuntimeSeconds) * time.Second; limit == 0 || t < limit {
			limit = t
		}
	}
	return limit
}

// OneTimeNotices records notices that are shown once per install.
type OneTimeNotices interface {
	Shown(key string) bool
	Mark(key string) error
}

// UseNotices keeps one-time notices in n, which outlives the session; the
// default keeps them for this Agency only.
func (a *Agency) UseNotices(n OneTimeNotices) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notices = n
}

// noteOwnWorkDefault tells the user, once per persona, that a persona on a
// hosted backend does its own jobs by default even though Settings chose a
// worker. It is an info line only: never spoken and never sent to a model.
func (a *Agency) noteOwnWorkDefault(d workDecision) {
	cfg := d.bound
	if d.own == nil || cfg == nil || strings.TrimSpace(cfg.PersonaWork) != "" || !brain.IsHosted(providerName(d.own)) || !config.SettingsWorkerChosen(cfg) {
		return
	}
	a.mu.Lock()
	notices := a.notices
	a.mu.Unlock()
	key := "own-work-default:" + personaKey(cfg.ActivePersona)
	if notices == nil || notices.Shown(key) {
		return
	}
	id := strings.TrimSpace(cfg.ActivePersona)
	a.pipeline.emit(events.Info{Message: fmt.Sprintf("%s does its background jobs on its own backend, %s, not on the worker chosen in Settings. To hand them to that worker: obey-voice persona edit %s --work worker",
		personaLabel(cfg), providerName(d.own), id)})
	if err := notices.Mark(key); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("could not record that notice: %v", err)})
	}
}

// ownTierBudget holds a local own job to the routed tier's request, tool,
// retry and output limits and to the strict allowlist, as a local tier worker
// is held. A hosted backend runs its own tools and requests, which the host
// cannot count, so like a hosted tier worker it gets the runtime limit only;
// under the strict policy resolveWork refuses it instead.
func ownTierBudget(cfg *config.Config, tier *config.OwnTier) *brain.WorkerBudget {
	if tier == nil || providerName(cfg) != "ollama" {
		return nil
	}
	l := tier.Limits
	limits := brain.BudgetLimits{StrictTools: tier.Strict, MaxRequests: l.ModelRequests, MaxToolCalls: l.ToolCalls, MaxRetries: l.TransportRetries, MaxRuntime: time.Duration(l.RuntimeSeconds) * time.Second}
	if l.OutputTokens != nil {
		limits.MaxOutputTokens = *l.OutputTokens
	}
	return brain.NewWorkerBudget(limits)
}

// ownWorkRun builds the persona's own backend as a worker on the job's
// goroutine: a provider of its own, separate from the one the persona talks
// on, so the talking session is never resumed or filled with the job's tool
// traffic. It then runs like any legacy worker, under the routed tier's
// limits in tiered mode.
func (a *Agency) ownWorkRun(brief string, cfg *config.Config, tier *config.OwnTier) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		budget := legacyWorkerBudget(a.workEnabled(), cfg)
		if tier != nil {
			budget = ownTierBudget(cfg, tier)
			if budget != nil {
				ctx = budget.Context(ctx)
			}
		}
		worker, err := a.buildOwnWorker(ctx, cfg)
		if err != nil {
			return "", err
		}
		defer func() { _ = brain.CloseProvider(worker) }()
		return a.workerRunWithBudget(brief, worker, cfg, budget)(ctx)
	}
}

func (a *Agency) buildOwnWorker(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
	factory := a.ownFactory
	if factory == nil {
		factory = newOwnWorker
	}
	worker, err := factory(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("prepare %s for its own work: %w", providerName(cfg), err)
	}
	return worker, nil
}

func newOwnWorker(_ context.Context, cfg *config.Config) (brain.Provider, error) {
	return brain.NewWorkerProvider(cfg)
}
