package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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
		brief, control := steer.begin(ctx, brief, worker)
		opts.Control = control
		a.saveWorkerThread(id, []brain.Turn{{Role: "user", Content: brief, Attachments: refs}})
		r := workerRun{a: a, ctx: ctx, id: id, brief: brief, worker: worker, cfg: cfg, budget: budget, steer: steer,
			tools: tools, allowImageRepair: allowImageRepair, workDir: config.WorkDirFrom(cfg)}
		r.warn = func(message string) { a.pipeline.emit(events.Info{Message: message}) }
		if control == nil {
			// Set, never cleared, so an image-repair retry keeps the first reason.
			opts.OnForcedAnswer = func(reason brain.ForcedAnswer) { a.noteForcedAnswer(id, reason) }
			start, end := opts.OnToolStart, opts.OnToolEnd
			opts.OnToolStart = func(call brain.ToolCall) { a.captureWorkerThread(id, worker); start(call) }
			opts.OnToolEnd = func(result brain.ToolResult) { end(result); a.captureWorkerThread(id, worker) }
			defer a.captureWorkerThread(id, worker)
			r.think = func(input string) (string, error) { return worker.ThinkFull(ctx, input, opts) }
			firstReply, err := r.think(brief)
			if err != nil {
				return firstReply, err
			}
			done := r.finish(firstReply)
			return done.text, done.err
		}
		// Versioned checkpoints replace the hook snapshots, which a history a
		// failed request rolled back could overwrite.
		opts.OnCheckpoint = func() { steer.saveCheckpoint(worker) }
		return r.steered(control, opts)
	}
}

// workerRun is one job's run on its worker, from the first reply to the
// answer it delivers.
type workerRun struct {
	a                *Agency
	ctx              context.Context
	id, brief        string
	worker           brain.Provider
	cfg              *config.Config
	budget           *brain.WorkerBudget
	steer            *jobSteer
	tools            bool
	allowImageRepair bool
	workDir          string
	warn             func(string)
	think            func(input string) (string, error)
	// forced is why the answer being finished was forced, if it was.
	forced brain.ForcedAnswer
}

// finished is a reply after its images were imported, and repaired if they
// had to be.
type finished struct {
	text string
	err  error
	// preempted: a correction arrived before the reply was finished, so the
	// job may continue from it instead.
	preempted bool
	// noImage: the task asked for an image, a correction was given, and no
	// image came back. It is not an error, but it is said.
	noImage bool
	// media are the images the reply attached.
	media []chatmedia.Ref
}

// steered runs a job that takes corrections. The runner, not the worker loop,
// commits the answer: a correction can arrive while a reply is imported or
// repaired. Then the job continues from that correction with the same worker
// and budget, holding the answer in hand until a replacement succeeds or a
// limit leaves no request for one.
func (r *workerRun) steered(control *brain.WorkerControl, opts brain.StreamOptions) (string, error) {
	start := time.Now()
	r.think = func(input string) (string, error) {
		var got brain.ForcedAnswer
		opts.OnForcedAnswer = func(reason brain.ForcedAnswer) {
			if got == "" {
				got = reason
			}
		}
		before := control.Included()
		reply, err := r.worker.ThinkFull(r.ctx, input, opts)
		// The reason belongs to the answer's revision: one written after a
		// correction is judged on its own, while an uncorrected repair keeps
		// the first reason.
		if err == nil && (control.Included() > before || r.forced == "") {
			r.forced = got
		}
		return reply, err
	}
	var held *finished
	var heldForced brain.ForcedAnswer
	reply, err := r.think(r.brief)
	for {
		if err != nil {
			if errors.Is(r.ctx.Err(), context.Canceled) {
				// The job table ended the job; its ending settles the corrections.
				return reply, err
			}
			control.Close(brain.NotAppliedReason(err))
			if held == nil {
				return reply, err
			}
			// The corrected run failed; the answer it would have replaced stands.
			r.forced = cmp.Or(heldForced, failedCorrectionReason(err, r.budget))
			return r.deliverHeld(*held)
		}
		done := r.finish(reply)
		if done.err != nil && !done.preempted {
			control.Close(brain.NotAppliedReason(done.err))
			// A replacement counts only once it is finished; one whose
			// images could not be attached leaves the answer in hand.
			if held != nil && !errors.Is(r.ctx.Err(), context.Canceled) {
				r.forced = cmp.Or(heldForced, brain.ForcedByFailedCorrection)
				return r.deliverHeld(*held)
			}
			return done.text, done.err
		}
		sealed, limit := control.Seal(brain.WorkerLimitDue(r.ctx, r.budget, start))
		if sealed {
			if limit != "" && r.forced == "" {
				r.forced = limit
			}
			return r.deliver(done)
		}
		held, heldForced = &done, r.forced
		r.forced = ""
		reply, err = r.think("")
	}
}

// deliver notes how the answer was finished and saves the thread it was
// imported into; delivery reads the reply's images from that thread.
func (r *workerRun) deliver(done finished) (string, error) {
	r.steer.saveCheckpoint(r.worker)
	return r.note(done)
}

// note records how the answer was finished and returns it, marked when it
// came back without the image its task asked for.
func (r *workerRun) note(done finished) (string, error) {
	if r.forced != "" {
		r.a.noteForcedAnswer(r.id, r.forced)
	}
	if done.noImage && done.err == nil {
		r.a.noteNoImage(r.id)
		done.text = strings.TrimRight(done.text, "\n") + "\n\n" + noImageMarker
	}
	return done.text, done.err
}

// deliverHeld delivers the answer held while a correction ran, after that run
// failed. The worker's history was rolled back past the corrected run, so the
// saved thread keeps its newest evidence and the answer's images are kept
// apart from it for delivery.
func (r *workerRun) deliverHeld(held finished) (string, error) {
	r.a.keepReplyMedia(r.id, held.media)
	return r.note(held)
}

// noImageMarker ends an answer that came back without the image its task
// asked for, after a correction.
const noImageMarker = "(No image attached.)"

// failedCorrectionReason is why an answer held while a correction ran is
// delivered cut short when that run failed.
func failedCorrectionReason(err error, budget *brain.WorkerBudget) brain.ForcedAnswer {
	switch {
	case errors.Is(err, brain.ErrWorkerOutOfTime), errors.Is(err, context.DeadlineExceeded):
		return brain.ForcedByTime
	case errors.Is(err, brain.ErrWorkerBudgetExceeded):
		if left, counted := budget.RemainingRequests(); counted && left == 0 {
			return brain.ForcedByRequests
		}
	}
	return brain.ForcedByFailedCorrection
}

// finish imports the reply's images and, when an image the task asked for is
// missing, asks the worker once more, with the brief and the corrections it
// was given in order. A correction waiting before the repair or a failure is
// reported preempts both. After a correction, an answer still without the
// image is delivered marked instead of failed: the correction may have
// dropped it.
func (r *workerRun) finish(reply string) finished {
	ctx, worker := r.ctx, r.worker
	asked, _ := ctx.Value(imageDeliveryKey{}).(bool)
	waiting := func() bool { return r.steer != nil && r.steer.control.Waiting() }
	var media []chatmedia.Ref
	finalize := func(result string) (string, error) {
		cleaned, attached, err := finalizeProviderReply(worker, result, r.workDir, r.warn)
		if len(attached) > 0 {
			media = attached
			return cleaned, nil
		}
		if asked && err == nil {
			err = errNoImageDelivered
		}
		if !asked && err != nil && chatmedia.ReplyProse(result) != "" {
			return cleaned, nil
		}
		return cleaned, err
	}
	without := func(text, result string, failure error) finished {
		if r.steer.corrected() && chatmedia.ReplyProse(result) != "" {
			return finished{text: text, noImage: true}
		}
		return finished{text: text, err: failure}
	}
	cleaned, importErr := finalize(reply)
	if importErr == nil {
		return finished{text: cleaned, media: media}
	}
	if err := ctx.Err(); err != nil {
		return finished{err: err}
	}
	attachmentErr := workerImageFailure(importErr, reply)
	if waiting() {
		return finished{text: cleaned, err: attachmentErr, preempted: true}
	}
	remaining, limited := r.budget.RemainingRequests()
	// An opaque paid dispatch has a single admission and no per-request budget.
	if !r.tools || !r.allowImageRepair || (limited && remaining == 0) || (r.budget == nil && !servedLocally(ctx, r.cfg, worker)) {
		return without(cleaned, reply, attachmentErr)
	}
	input, promptErr := prompts.RenderSystem(config.PromptsDirFrom(r.cfg), "worker-image-repair", map[string]string{
		"brief": briefWithCorrections(r.brief, r.steer.runningCorrections()), "work_dir": r.workDir, "import_error": fmt.Sprintf("%.512s", importErr.Error()),
	})
	if promptErr != nil {
		r.warn(promptErr.Error())
		if strings.TrimSpace(input) == "" {
			return without(cleaned, reply, attachmentErr)
		}
	}
	retryReply, err := r.think(input)
	if errors.Is(err, brain.ErrWorkerBudgetExceeded) {
		return without(cleaned, reply, attachmentErr)
	}
	if err != nil {
		return finished{text: retryReply, err: workerImageFailure(err, reply, retryReply)}
	}
	cleaned, importErr = finalize(retryReply)
	if importErr != nil {
		failure := workerImageFailure(importErr, reply, retryReply)
		if waiting() {
			return finished{text: cleaned, err: failure, preempted: true}
		}
		return without(cleaned, retryReply, failure)
	}
	return finished{text: cleaned, media: media}
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
