package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// Agency wires spawn/abort/status routes to the async job table and worker brain.
type Agency struct {
	pipeline    *Pipeline
	jobs        *job.Table
	worker      brain.Provider
	cfg         *config.Config
	planFactory func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error)
	// ownFactory builds the persona's own backend for work it does itself.
	ownFactory func(context.Context, *config.Config) (brain.Provider, error)
	// ownJobDefault caps a hosted persona's own job while
	// voice_worker_job_budget is unset.
	ownJobDefault time.Duration
	narrator      *Narrator
	// work folds a job's tool calls into rows: what "what's happening" answers
	// with, and what milestone narration speaks one line of.
	work *workfold.Tracker

	mu             sync.Mutex
	pending        []pendingDelivery
	workerReplies  []workerReply
	workerThreads  map[string][]brain.Turn
	workerOrigins  map[string]workerOrigin
	workerInitErr  error
	workMu         sync.RWMutex
	coordinator    *work.Coordinator
	workScope      work.WorkScope
	workObserver   func(work.Snapshot)
	jobScopes      map[string]work.WorkScope
	managed        map[*delegation.Runtime]struct{}
	managedJobs    map[string]*delegation.Runtime
	managedCurrent *delegation.Runtime
	managedClosing map[*delegation.Runtime]bool
	liveManaged    func() *delegation.Runtime // guarded by mu; provider accessor is concurrency-safe
	personas       map[string]bool            // guarded by mu
	notices        OneTimeNotices             // guarded by mu
	// heldReplies wait for their speech to start before history may take
	// them, so the saved thread follows the order the user heard.
	heldReplies map[string]workerReply
}

// NewAgency constructs the spawn coordinator. worker may be nil when construction
// failed; spawn will surface the error at job start.
func NewAgency(p *Pipeline, jobs *job.Table, worker brain.Provider, cfg *config.Config, workerErr error) *Agency {
	a := &Agency{
		pipeline:       p,
		jobs:           jobs,
		worker:         worker,
		cfg:            cloneConfig(cfg),
		planFactory:    brain.NewPlannedWorker,
		ownFactory:     newOwnWorker,
		ownJobDefault:  time.Duration(config.DefaultHostedOwnJobBudget) * time.Second,
		notices:        config.NewNotices(""),
		work:           workfold.New(),
		workerThreads:  make(map[string][]brain.Turn),
		workerOrigins:  make(map[string]workerOrigin),
		workerInitErr:  workerErr,
		jobScopes:      make(map[string]work.WorkScope),
		managed:        make(map[*delegation.Runtime]struct{}),
		managedJobs:    make(map[string]*delegation.Runtime),
		managedClosing: make(map[*delegation.Runtime]bool),
	}
	if jobs != nil && tieredWorkers(cfg) {
		jobs.SetWorkerLimits(cfg.VoiceWorkers.Policy.ConcurrentWorkers, cfg.VoiceWorkers.Policy.QueuedWorkers)
	}
	a.narrator = NewNarrator(a)
	if jobs != nil && cfg != nil {
		jobs.SetEnabled(!cfg.VoiceWorkersDisabled)
	}
	a.notePersonaLocked(a.cfg)
	return a
}

// Wire attaches route handlers and job callbacks to the pipeline.
func (a *Agency) Wire() {
	if a == nil || a.pipeline == nil || a.jobs == nil {
		return
	}
	a.pipeline.Agency = a
	a.bindConversationWorkers(a.pipeline.Brain)
	a.pipeline.JobActive = a.jobs.Active
	a.jobs.OnDone(a.onJobDone)
	a.jobs.OnQueued(a.onJobQueued)
	a.jobs.OnStart(a.onJobStarted)
	if a.narrator != nil {
		a.narrator.Wire()
	}
}

// HandleRoute executes spawn, job.abort, and job.status routes. OnRoute fires
// exactly once per call, after the handler, for every kind — including
// job.status and a spawn whose job failed to start — so an observer sees one
// decision per routed turn.
func (a *Agency) HandleRoute(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if a == nil {
		return
	}
	if d.Kind != RouteSpawn {
		a.emitRouteDecision(d, "handled", "", "")
	}
	switch d.Kind {
	case RouteSpawn:
		a.handleSpawn(ctx, turn, metrics, d)
	case RouteNarrate, RouteNarratePause, RouteNarrateResume, RouteNarrateSkip, RouteNarrateStop:
		if a.workEnabled() && d.Kind == RouteNarrate && wantsWrittenStory(d.Transcript, config.WorkDirFrom(a.currentConfig())) {
			a.pipeline.emit(events.Error{Stage: "job", Message: "Story-writing narration is unavailable with the experimental work inbox. Existing files can still be read aloud."})
			return
		}
		if a.narrator != nil {
			a.narrator.HandleRoute(ctx, turn, metrics, d)
		}
	case RouteJobAbort:
		a.handleAbort(ctx, turn, metrics)
	case RouteJobStatus:
		a.speakJobStatus(ctx, turn, metrics)
	}
	if a.pipeline.OnRoute != nil {
		a.pipeline.OnRoute(d)
	}
}

func (a *Agency) workerProvider() (brain.Provider, error) {
	experimental := a.workEnabled()
	a.mu.Lock()
	defer a.mu.Unlock()
	if experimental && providerName(a.cfg) != "ollama" {
		return nil, errors.New("experimental work requires a local worker")
	}
	if a.worker != nil {
		return a.worker, nil
	}
	if a.workerInitErr != nil {
		return nil, a.workerInitErr
	}
	return nil, fmt.Errorf("worker provider is not configured")
}

func (a *Agency) currentConfig() *config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneConfig(a.cfg)
}

func (a *Agency) workerAdmission() (brain.Provider, *config.Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.workerInitErr
	if a.worker == nil && err == nil {
		err = errors.New("worker provider is not configured")
	}
	return a.worker, cloneConfig(a.cfg), err
}

func (a *Agency) AfterTurn() {
	if a == nil {
		return
	}
	if a.narrator != nil {
		a.narrator.tryFlushPending()
	}
	a.tryFlushPending()
}
