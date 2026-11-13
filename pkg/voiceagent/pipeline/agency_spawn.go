package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// spawnClarifier is what the assistant says instead of spawning on an assent
// that has no conversation behind it.
const spawnClarifier = "I did not catch what you would like me to work on. What should I do?"

func (a *Agency) handleSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if err := a.jobs.CheckAdmission(); err != nil {
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	task := strings.TrimSpace(d.Transcript)
	history := a.liveHistory()

	// An assent with nothing behind it is not a task. Spawning on it hands a
	// worker with shell tools a prompt like "Yes." and lets it decide what
	// that meant — which is exactly how one went looking through the CLI's
	// session store and acted on somebody else's thread.
	if spawnLacksTask(history, task) {
		a.emitRouteDecision(d, "rejected", "", spawnClarifier)
		a.speakSpawnClarifier(ctx, turn, metrics)
		a.pipeline.emit(events.Info{Message: "spawn declined: no task in the utterance and no conversation behind it"})
		return
	}

	if task == "" {
		task = "background task"
	}
	experimental := a.workEnabled()
	// A persona doing its own work runs on its own backend, held to the
	// routed tier's limits in tiered mode; the configured worker and the
	// tier's assignment apply only to work handed to the worker.
	decision := a.turnWork(ctx)
	jobCfg, own := decision.own, decision.own != nil
	var worker brain.Provider
	var plan *config.WorkerPlan
	var ownTier *config.OwnTier
	if own {
		var err error
		if ownTier, err = decision.admit(d.Tier); err != nil {
			a.emitRouteDecision(d, "rejected", "", err.Error())
			a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
			return
		}
	} else {
		var workerErr error
		worker, jobCfg, plan, workerErr = a.admitWorker(d.Tier)
		if workerErr != nil {
			a.emitRouteDecision(d, "rejected", "", workerErr.Error())
			a.pipeline.emit(events.Error{Stage: "job", Message: workerErr.Error()})
			return
		}
	}
	if experimental && plan == nil && providerName(jobCfg) != "ollama" {
		a.emitRouteDecision(d, "rejected", "", "Experimental work requires a local worker; paid and unknown providers are disabled.")
		a.pipeline.emit(events.Error{Stage: "job", Message: "Experimental work requires a local worker; paid and unknown providers are disabled."})
		return
	}
	// The job sends the persona's share to its hosted backend; the user is
	// told first, as before any other request to it.
	if own && brain.IsHosted(providerName(jobCfg)) {
		if err := a.pipeline.announceHosted(ctx, metrics); err != nil {
			a.emitRouteDecision(d, "rejected", "", err.Error())
			return
		}
	}
	jobID := ""
	var key work.JobKey
	admission := frozenWorkerConfig(jobCfg, plan)
	if own {
		admission["work"] = config.WorkOwn
		if ownTier != nil {
			admission["worker_tier"] = string(ownTier.Tier)
		}
	}
	if c, scope := a.workAuthority(); c != nil {
		jobID = newWorkID()
		var err error
		key, err = c.Register(scope, work.Admission{JobID: jobID, Task: task, Origin: "agency", Provider: providerName(jobCfg), Model: modelName(jobCfg), Config: admission})
		if err != nil {
			a.emitRouteDecision(d, "rejected", "", err.Error())
			a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
			return
		}
		a.workMu.Lock()
		a.jobScopes[jobID] = scope
		a.workMu.Unlock()
	}
	brief := spawnBrief(history, d.Transcript, briefPersonaID(metrics, jobCfg))
	run := a.workerRun(brief, worker, jobCfg)
	switch {
	case plan != nil:
		run = a.plannedWorkerRun(brief, jobCfg, plan)
	case own:
		run = a.ownWorkRun(brief, jobCfg, ownTier)
	}
	refs := chatmedia.Input(ctx)
	if len(refs) > 0 {
		originalRun := run
		run = func(jobCtx context.Context) (string, error) { return originalRun(chatmedia.WithInput(jobCtx, refs)) }
	}
	run = withLiveSkills(a.pipeline.Brain, run)
	var runtimeBudget time.Duration
	if plan != nil {
		runtimeBudget = time.Duration(plan.Limits.RuntimeSeconds) * time.Second
	}
	if own {
		runtimeBudget = a.ownRuntimeBudget(decision, ownTier)
	}
	origin := workerOrigin{Scope: a.pipeline.ConversationScope(), Trace: events.TraceFromContext(ctx), admission: admission}
	if metrics != nil {
		origin.Envelope = metrics.response
		origin.Trace = metrics.trace
	}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = a.pipeline.responseTTS(ctx)
	origin.releaseVoice = a.pipeline.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	acceptedID, err := a.jobs.Start(ctx, job.Spec{
		RuntimeBudget: runtimeBudget,
		ID:            jobID,
		OnAccepted: func(id string) {
			a.mu.Lock()
			a.workerOrigins[id] = origin
			a.mu.Unlock()
			a.emitRouteDecision(d, "accepted", id, "")
		},
		// Task is the label the user hears and sees; the brief is what the
		// worker reads. They were the same string, which is why the worker
		// only ever received one utterance.
		Task:     task,
		Provider: providerName(jobCfg),
		Model:    modelName(jobCfg),
		Run:      run,
	})
	if err != nil {
		origin.releaseVoice()
		a.emitRouteDecision(d, "rejected", jobID, err.Error())
		if c, _ := a.workAuthority(); c != nil && key.JobID != "" {
			_, _ = c.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeFailed, Text: truncateJobTask(err.Error()), Termination: "known"})
		}
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	if own {
		a.noteOwnWorkDefault(decision)
	}
	// A routed turn must not silently end while its work continues. Speak only
	// after admission succeeded; the same bounded line is retained in canonical
	// history even for muted/text-only hosts. This does not wait for the worker.
	if turn != nil && metrics != nil {
		line := "I'll work on that and report back."
		for _, info := range a.jobs.Status() {
			if info.ID == acceptedID && info.State == job.StateQueued {
				line = "I've queued that task and will report back."
				break
			}
		}
		if err := a.pipeline.speakStandalone(ctx, turn, metrics, line); err != nil {
			metrics.degraded = true
		}
	}
}

// liveHistory is the talking brain's conversation, or nil when there is no
// brain to ask. A worker that gets nothing is handled by spawnLacksTask. A
// provider that sends a hosted service only its persona's share gives the
// worker only that share.
func (a *Agency) liveHistory() []brain.Turn {
	if a.pipeline == nil || a.pipeline.Brain == nil {
		return nil
	}
	if live := a.pipeline.Brain; brain.HostedSharedHistory(live) {
		return live.(brain.ContinuityTransferProvider).ContinuitySnapshot().Working
	}
	return a.pipeline.Brain.History()
}

// withLiveSkills gives a job the skills the talking brain activated for the
// conversation it is spawned from, so the worker does not choose again.
func withLiveSkills(live brain.Provider, run func(context.Context) (string, error)) func(context.Context) (string, error) {
	provider, ok := live.(brain.ActivatedSkillsProvider)
	if !ok {
		return run
	}
	names := provider.RecentActivatedSkills()
	if len(names) == 0 {
		return run
	}
	return func(jobCtx context.Context) (string, error) {
		return run(brain.WithActivatedSkills(jobCtx, names))
	}
}

// speakSpawnClarifier asks rather than guesses.
func (a *Agency) speakSpawnClarifier(ctx context.Context, turn *turnConductor, metrics *turnMetrics) {
	if err := a.pipeline.speakStandalone(ctx, turn, metrics, spawnClarifier); err != nil {
		a.pipeline.emit(events.Info{Message: fmt.Sprintf("clarifier speech failed: %v", err)})
	}
}

func truncateJobTask(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 120 {
		return s
	}
	return s[:117] + "..."
}
