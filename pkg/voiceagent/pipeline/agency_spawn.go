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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// spawnClarifier is what the assistant says instead of spawning on an assent
// that has no conversation behind it.
const spawnClarifier = "I did not catch what you would like me to work on. What should I do?"

func (a *Agency) handleSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision) {
	if work := a.turnWork(ctx); work.inline {
		a.refuseInlineSpawn(ctx, turn, metrics, d, work)
		return
	}
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
	w, err := a.admitSpawn(ctx, d.Tier, a.workerSource())
	if err != nil {
		a.emitRouteDecision(d, "rejected", "", err.Error())
		a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
		return
	}
	jobCfg, own, decision := w.cfg, w.own, w.decision
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
	admission := w.admission()
	// Corrections can reach the job from the moment anyone can name it.
	admitted := a.pipeline.ConversationScope()
	if _, scope := a.workAuthority(); scope.Valid() {
		admitted = scope
	}
	steer := a.newJobSteer(admitted)
	if c, scope := a.workAuthority(); c != nil {
		jobID = newWorkID()
		a.registerSteer(jobID, steer)
		a.workMu.Lock()
		a.jobScopes[jobID] = scope
		a.workMu.Unlock()
		var err error
		key, err = c.Register(scope, work.Admission{JobID: jobID, Task: task, Origin: "agency", Provider: providerName(jobCfg), Model: modelName(jobCfg), Config: admission})
		if err != nil {
			a.workMu.Lock()
			delete(a.jobScopes, jobID)
			a.workMu.Unlock()
			a.unregisterSteer(jobID, steer)
			a.emitRouteDecision(d, "rejected", "", err.Error())
			a.pipeline.emit(events.Error{Stage: "job", Message: err.Error()})
			return
		}
	}
	run, runtimeBudget := a.spawnRun(ctx, metrics, d.Transcript, task, history, a.pipeline.Brain, w)
	spawned := run
	run = func(jobCtx context.Context) (string, error) { return spawned(withJobSteer(jobCtx, steer)) }
	origin := workerOrigin{Scope: a.pipeline.ConversationScope(), Trace: events.TraceFromContext(ctx), admission: admission}
	if metrics != nil {
		origin.Envelope = metrics.response
		origin.Trace = metrics.trace
	}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = a.pipeline.responseTTS(ctx)
	origin.releaseVoice = a.pipeline.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	var register func(string)
	if jobID == "" {
		register = func(id string) { a.registerSteer(id, steer) }
	}
	spec := job.Spec{
		RuntimeBudget: runtimeBudget,
		ID:            jobID,
		Register:      register,
		OnEnding:      steer.decide,
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
	}
	model, local := workerLocalModel(jobCfg)
	a.placeJob(ctx, &spec, model, local)
	acceptedID, err := a.jobs.Start(ctx, spec)
	if err != nil {
		origin.releaseVoice()
		if jobID != "" {
			a.unregisterSteer(jobID, steer)
		}
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

// inlineSpawnLine is what a persona that works inline says instead of
// handing work to a background job.
const inlineSpawnLine = "I work right here in the conversation and don't hand work to background jobs. Ask me again and I'll answer here."

// refuseInlineSpawn answers a spawn route from a persona that works inline:
// no job starts, the reason is shown, and the persona says so.
func (a *Agency) refuseInlineSpawn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, d RouteDecision, work workDecision) {
	d.ReasonCode = events.RouteReasonInlineWork
	a.emitRouteDecision(d, "rejected", "", work.inlineRefusal().Error())
	if turn != nil && metrics != nil {
		if err := a.pipeline.speakStandalone(ctx, turn, metrics, inlineSpawnLine); err != nil {
			metrics.degraded = true
		}
	}
}

// userSentence is an error whose text is shown to the user as written.
type userSentence string

func (s userSentence) Error() string { return string(s) }

const errExperimentalPaidWork = userSentence("Experimental work requires a local worker; paid and unknown providers are disabled.")

// spawnWork is who runs a spawned job: the persona itself, under its routed
// tier, or a worker admitted from the configuration at hand.
type spawnWork struct {
	decision workDecision
	own      bool
	cfg      *config.Config
	worker   brain.Provider
	plan     *config.WorkerPlan
	ownTier  *config.OwnTier
}

// admitSpawn decides who runs work routed at tier. A persona doing its own
// work runs on its own backend, held to the routed tier's limits in tiered
// mode; the configured worker and the tier's assignment apply only to work
// handed to the worker.
func (a *Agency) admitSpawn(ctx context.Context, tier config.WorkerTierName, source workerSource) (spawnWork, error) {
	decision := a.turnWork(ctx)
	w := spawnWork{decision: decision, own: decision.own != nil, cfg: decision.own}
	if decision.inline {
		return w, decision.inlineRefusal()
	}
	var err error
	if w.own {
		w.ownTier, err = decision.admit(tier)
	} else {
		w.worker, w.cfg, w.plan, err = a.admitWorkerFrom(source, tier)
	}
	if err != nil {
		return w, err
	}
	if a.workEnabled() && w.plan == nil && providerName(w.cfg) != "ollama" {
		return w, errExperimentalPaidWork
	}
	return w, nil
}

func (w spawnWork) admission() map[string]string {
	admission := frozenWorkerConfig(w.cfg, w.plan)
	if w.own {
		admission["work"] = config.WorkOwn
		if w.ownTier != nil {
			admission["worker_tier"] = string(w.ownTier.Tier)
		}
	}
	return admission
}

// spawnRun is a spawned job's run function, briefed from history, and the
// runtime limit it is admitted with; zero leaves the job table's.
func (a *Agency) spawnRun(ctx context.Context, metrics *turnMetrics, transcript, task string, history []brain.Turn, live brain.Provider, w spawnWork) (func(context.Context) (string, error), time.Duration) {
	user := ""
	if w.cfg != nil {
		user = w.cfg.UserDisplayName
	}
	brief := spawnBrief(history, transcript, briefPersonaID(metrics, w.cfg), a.pipeline.SpeakerNames, user)
	imageTask := requestsChatImage(task, history)
	if imageTask {
		instruction, err := prompts.RenderSystem(config.PromptsDirFrom(w.cfg), "worker-image-delivery", nil)
		if err != nil {
			a.pipeline.emit(events.Info{Message: err.Error()})
		}
		brief += "\n" + instruction
	}
	run := a.workerRun(brief, w.worker, w.cfg)
	switch {
	case w.plan != nil:
		run = a.plannedWorkerRun(brief, w.cfg, w.plan)
	case w.own:
		run = a.ownWorkRun(brief, w.cfg, w.ownTier)
	}
	refs := chatmedia.Input(ctx)
	if len(refs) > 0 || imageTask {
		originalRun := run
		run = func(jobCtx context.Context) (string, error) {
			if imageTask {
				jobCtx = context.WithValue(jobCtx, imageDeliveryKey{}, true)
			}
			return originalRun(chatmedia.WithInput(jobCtx, refs))
		}
	}
	run = withLiveSkills(live, run)
	var runtimeBudget time.Duration
	if w.plan != nil {
		runtimeBudget = time.Duration(w.plan.Limits.RuntimeSeconds) * time.Second
	}
	if w.own {
		runtimeBudget = a.ownRuntimeBudget(w.decision, w.ownTier)
	}
	return run, runtimeBudget
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
