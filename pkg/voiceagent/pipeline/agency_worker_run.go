package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// workNarrationMilestones is the voice_work_narration setting that speaks one
// line per closed fold row. The default, status_only, speaks nothing until the
// user asks what is happening.
const workNarrationMilestones = "milestones"

var errNoImageDelivered = errors.New("the worker returned text without an image attachment")

func (a *Agency) workerRun(brief string, worker brain.Provider, cfg *config.Config) func(context.Context) (string, error) {
	return a.workerRunWithBudget(brief, worker, cfg, legacyWorkerBudget(a.workEnabled(), cfg), true)
}

// A worker with no request budget may repeat a request only when its model
// runs on this machine. The provider name does not prove that: Ollama also
// serves cloud aliases, and the admission check that rejects them runs only
// for feature-enabled work.
func servedLocally(ctx context.Context, cfg *config.Config, worker brain.Provider) bool {
	if providerName(cfg) != "ollama" {
		return false
	}
	validator, ok := worker.(brain.LegacyWorkerValidator)
	return !ok || validator.ValidateLegacyWorker(ctx) == nil
}

func (a *Agency) workerRunWithBudget(brief string, worker brain.Provider, cfg *config.Config, budget *brain.WorkerBudget, allowImageRepair bool) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if a.workEnabled() && !a.acceptJobCallback(job.IDFromContext(ctx)) {
			return "", context.Canceled
		}
		if worker == nil {
			return "", errors.New("worker provider is not configured")
		}
		worker = a.freshWorker(worker)
		if end, ok := job.BudgetEnd(ctx); ok {
			ctx = brain.WithWorkerDeadline(ctx, end)
		}
		if a.workEnabled() && !tieredWorkers(cfg) && providerName(cfg) == "ollama" {
			if validator, ok := worker.(brain.LegacyWorkerValidator); ok {
				if err := validator.ValidateLegacyWorker(ctx); err != nil {
					return "", fmt.Errorf("validate local worker: %w", err)
				}
			}
		} else if !tieredWorkers(cfg) {
			if validator, ok := worker.(brain.WorkerToolValidator); ok {
				if err := validator.ValidateWorkerTools(ctx); err != nil {
					return "", fmt.Errorf("validate worker tools: %w", err)
				}
			}
		}
		a.bindWorkerAdmission(worker)
		// Tiered admissions always run the full worker tool surface. The
		// conversation flag controls the live turn/router; it must not disable
		// tools for a worker that was explicitly admitted to a tier.
		tools := true
		if cfg != nil {
			tools = cfg.VoiceToolsEnabled || tieredWorkers(cfg)
		}
		refs := chatmedia.Input(ctx)
		if err := validateProviderImages(ctx, worker, refs); err != nil {
			return "", err
		}
		opts := brain.WorkerStreamOptions(tools)
		opts.Attachments = refs
		opts.WorkerBudget = budget
		opts.OnWorkerUsage = a.workerUsageHook(job.IDFromContext(ctx), budget)
		opts.OnToolStart, opts.OnToolEnd = a.foldingToolHooks(job.IDFromContext(ctx))
		id := job.IDFromContext(ctx)
		steer := jobSteerFrom(ctx)
		brief, control := steer.begin(brief, worker)
		opts.Control = control
		// The reason the delivered answer was forced, if it was. For a job
		// that takes corrections it is noted once the answer is sealed, since
		// a correction can still replace that answer.
		var forced brain.ForcedAnswer
		if control == nil {
			// Set, never cleared, so an image-repair retry keeps the first reason.
			opts.OnForcedAnswer = func(reason brain.ForcedAnswer) { a.noteForcedAnswer(id, reason) }
		} else {
			opts.OnForcedAnswer = func(reason brain.ForcedAnswer) {
				if forced == "" {
					forced = reason
				}
			}
		}
		a.saveWorkerThread(id, []brain.Turn{{Role: "user", Content: brief, Attachments: refs}})
		if control == nil {
			start, end := opts.OnToolStart, opts.OnToolEnd
			opts.OnToolStart = func(call brain.ToolCall) { a.captureWorkerThread(id, worker); start(call) }
			opts.OnToolEnd = func(result brain.ToolResult) { end(result); a.captureWorkerThread(id, worker) }
			defer a.captureWorkerThread(id, worker)
		} else {
			// Versioned checkpoints of complete steps replace the hook
			// snapshots, which can hold calls without results or a history
			// a failed request rolled back.
			opts.OnCheckpoint = func() { steer.saveCheckpoint(worker) }
		}
		workDir := config.WorkDirFrom(cfg)
		warn := func(message string) { a.pipeline.emit(events.Info{Message: message}) }
		firstReply, err := worker.ThinkFull(ctx, brief, opts)
		if control == nil {
			if err != nil {
				return firstReply, err
			}
			return a.finishWorkerReply(ctx, firstReply, brief, worker, cfg, budget, opts, steer, tools, allowImageRepair, workDir, warn)
		}
		// The runner, not the worker loop, commits the answer: a correction
		// may arrive while the reply is imported or repaired. Then the job
		// continues from that correction with the same worker and budget.
		for {
			if err != nil {
				control.Close(brain.NotAppliedReason(ctx, err))
				return firstReply, err
			}
			cleaned, finishErr := a.finishWorkerReply(ctx, firstReply, brief, worker, cfg, budget, opts, steer, tools, allowImageRepair, workDir, warn)
			if finishErr != nil {
				control.Close(brain.NotAppliedReason(ctx, finishErr))
				return cleaned, finishErr
			}
			sealed, limit := control.Seal()
			if sealed {
				if forced == "" {
					forced = limit
				}
				if forced != "" {
					a.noteForcedAnswer(id, forced)
				}
				// Delivery reads the reply's imported images from this thread.
				steer.saveCheckpoint(worker)
				return cleaned, nil
			}
			forced = ""
			firstReply, err = worker.ThinkFull(ctx, "", opts)
		}
	}
}

// finishWorkerReply imports the reply's images and, when an image the task
// required is missing, asks the worker once more. A corrected task may no
// longer want the image, so after a correction a missing one is not an error
// and is not repaired.
func (a *Agency) finishWorkerReply(ctx context.Context, firstReply, brief string, worker brain.Provider, cfg *config.Config, budget *brain.WorkerBudget, opts brain.StreamOptions, steer *jobSteer, tools, allowImageRepair bool, workDir string, warn func(string)) (string, error) {
	asked, _ := ctx.Value(imageDeliveryKey{}).(bool)
	finalize := func(result string) (string, error) {
		required := asked && !steer.corrected()
		cleaned, attached, err := finalizeProviderReply(worker, result, workDir, warn)
		if len(attached) > 0 {
			return cleaned, nil
		}
		if required && err == nil {
			err = errNoImageDelivered
		}
		if !required && err != nil && chatmedia.ReplyProse(result) != "" {
			return cleaned, nil
		}
		return cleaned, err
	}
	cleaned, importErr := finalize(firstReply)
	if importErr == nil {
		return cleaned, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	attachmentErr := workerImageFailure(importErr, firstReply)
	remaining, limited := budget.RemainingRequests()
	// An opaque paid dispatch has a single admission and no per-request budget.
	if !tools || !allowImageRepair || steer.corrected() || (limited && remaining == 0) || (budget == nil && !servedLocally(ctx, cfg, worker)) {
		return cleaned, attachmentErr
	}
	input, promptErr := prompts.RenderSystem(config.PromptsDirFrom(cfg), "worker-image-repair", map[string]string{
		"brief": brief, "work_dir": workDir, "import_error": fmt.Sprintf("%.512s", importErr.Error()),
	})
	if promptErr != nil {
		warn(promptErr.Error())
		if strings.TrimSpace(input) == "" {
			return cleaned, attachmentErr
		}
	}
	retryReply, err := worker.ThinkFull(ctx, input, opts)
	if errors.Is(err, brain.ErrWorkerBudgetExceeded) {
		return cleaned, attachmentErr
	}
	if err != nil {
		return retryReply, workerImageFailure(err, firstReply, retryReply)
	}
	cleaned, importErr = finalize(retryReply)
	if importErr != nil {
		return cleaned, workerImageFailure(importErr, firstReply, retryReply)
	}
	return cleaned, nil
}

// Failed jobs publish the error to chat. Keep the worker's explanation from
// each attempt there, even when the repair produced no answer of its own.
func workerImageFailure(cause error, replies ...string) error {
	var notes strings.Builder
	var previous string
	for _, reply := range replies {
		prose := chatmedia.ReplyProse(reply)
		if prose != "" && prose != previous {
			notes.WriteString("\nWorker reply: ")
			notes.WriteString(prose)
			previous = prose
		}
	}
	return &imageFailure{cause: cause, notes: notes.String()}
}

type imageFailure struct {
	cause error
	notes string
}

func (e *imageFailure) Error() string {
	return "could not attach the worker's images: " + e.cause.Error() + e.notes
}

func (e *imageFailure) Unwrap() error { return e.cause }

// freshWorker gives a job a conversation of its own: no history and no CLI
// session from the job before it, and nothing shared with a job running
// alongside. A provider that cannot fork is cleared instead, and only while
// it is the one job running, so a neighbour's turn is not wiped from under it.
func (a *Agency) freshWorker(w brain.Provider) brain.Provider {
	if f, ok := w.(brain.FreshProvider); ok {
		return f.Fresh()
	}
	running := 0
	for _, info := range a.jobs.Status() {
		if info.State == job.StateRunning {
			running++
		}
	}
	if running <= 1 {
		w.ClearHistory()
	}
	return w
}

// foldingToolHooks emits the job's tool events and folds them on the way past.
// Feeding the tracker here rather than from a bus subscription keeps the fold
// in step with the emit — the closed row milestone narration speaks is the
// return of the same Add — and needs no bus to exist.
func (a *Agency) foldingToolHooks(jobID string) (func(brain.ToolCall), func(brain.ToolResult)) {
	_, key, _ := a.callbackKey(jobID)
	start := func(call brain.ToolCall) {
		if a.workEnabled() && !a.acceptJobCallback(jobID) {
			return
		}
		event := toolStartedEvent(jobID, call)
		event.WorkScope = key.Scope
		a.pipeline.emit(event)
		if closed := a.work.Add(event); closed != nil {
			a.narrateRow(jobID, *closed)
		}
	}
	end := func(res brain.ToolResult) {
		if a.workEnabled() && !a.acceptJobCallback(jobID) {
			return
		}
		event := toolFinishedEvent(jobID, res)
		event.WorkScope = key.Scope
		a.pipeline.emit(event)
		a.work.Finish(event)
	}
	return start, end
}

// narrateRow speaks one short line for a row that just closed, under the same
// idle gate as a job summary. Silent unless voice_work_narration is milestones.
func (a *Agency) narrateRow(jobID string, row workfold.Row) {
	if a.workEnabled() {
		return
	}
	cfg := a.currentConfig()
	if cfg == nil || cfg.VoiceWorkNarration != workNarrationMilestones {
		return
	}
	a.enqueueSummary(jobID, capitalize(row.String())+".")
	a.tryFlushPending()
}

// closeFold ends the job's fold, speaking its last row under milestones.
// narrate is false for an abort: the cancel line or the budget line is all
// an abort says.
func (a *Agency) closeFold(jobID string, narrate bool) {
	rows := a.work.Seal(jobID)
	if !narrate || len(rows) == 0 {
		return
	}
	a.narrateRow(jobID, rows[len(rows)-1])
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
