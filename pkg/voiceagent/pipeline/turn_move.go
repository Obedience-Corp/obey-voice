package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// errTurnMoved ends a turn's reading of its model stream when the stream is
// handed to a background job instead of being stopped.
var errTurnMoved = errors.New("turn moved to the background")

// movableRequest is what a request its turn may hand to a background job
// keeps: the request itself, and the conversation as its provider held it
// before the request, which the host reads instead of the provider's own
// history while the request runs there.
type movableRequest struct {
	detach  func() bool
	input   string
	opts    brain.StreamOptions
	started time.Time
	before  brain.ContinuitySnapshot

	// Set by the move, under the recording's mu.
	jobID     string
	history   brain.ContinuitySnapshot
	workDir   string
	toolStart func(brain.ToolCall)
	toolEnd   func(brain.ToolResult)
	activity  func()
}

// moveRequest is typed input offered to a running turn. The turn answers on
// answer whether it moved.
type moveRequest struct {
	input  string
	answer chan bool
}

// turnMove is a running turn's offer to be moved. The turn takes requests
// only in its stream loop, so whether input names another persona is decided
// on the goroutine that owns the persona selector during the turn.
type turnMove struct {
	requests  chan moveRequest
	withdrawn chan struct{}
}

type turnMoves struct {
	mu    sync.Mutex
	offer *turnMove
	// moved are requests owned by background jobs whose provider has not
	// been retired yet.
	moved []*transcriptRecording
}

// moveOfferWait bounds how long MoveTurnToBackground waits for a turn that is
// busy outside its stream loop.
const moveOfferWait = 2 * time.Second

// MoveTurnToBackground asks the running turn to hand its model request to a
// background job instead of ending it, because input names another persona.
// It reports whether the turn accepted; when it did not, the caller stops the
// turn as before. A turn accepts only while it reads its model's reply on a
// provider whose request can be moved.
func (p *Pipeline) MoveTurnToBackground(input string) bool {
	p.moves.mu.Lock()
	offer := p.moves.offer
	p.moves.mu.Unlock()
	if offer == nil {
		return false
	}
	req := moveRequest{input: input, answer: make(chan bool, 1)}
	wait := time.NewTimer(moveOfferWait)
	defer wait.Stop()
	select {
	case offer.requests <- req:
		return <-req.answer
	case <-offer.withdrawn:
		return false
	case <-wait.C:
		return false
	}
}

// offerMove lets MoveTurnToBackground reach a turn while it reads rec's
// stream. The returned func withdraws the offer.
func (p *Pipeline) offerMove(rec *transcriptRecording) (<-chan moveRequest, func()) {
	if rec == nil || rec.movable == nil {
		return nil, func() {}
	}
	offer := &turnMove{requests: make(chan moveRequest), withdrawn: make(chan struct{})}
	p.moves.mu.Lock()
	p.moves.offer = offer
	p.moves.mu.Unlock()
	return offer.requests, sync.OnceFunc(func() {
		p.moves.mu.Lock()
		if p.moves.offer == offer {
			p.moves.offer = nil
		}
		p.moves.mu.Unlock()
		close(offer.withdrawn)
	})
}

// acceptMove answers req from the turn's stream loop.
func (p *Pipeline) acceptMove(req moveRequest) bool {
	ok := p.NamesOtherPersona != nil && p.Agency != nil && p.Agency.jobs.CheckAdmission() == nil && p.NamesOtherPersona(req.input)
	req.answer <- ok
	return ok
}

// movesRequests reports whether a request with opts may be moved: an
// ordinary user request on a provider keeping the shared history in this
// process, in a conversation whose host moves turns and whose jobs use the
// conversation's own delivery.
func (p *Pipeline) movesRequests(opts brain.StreamOptions) bool {
	if p.NamesOtherPersona == nil || p.Agency == nil || p.Agency.workEnabled() || p.Native != nil {
		return false
	}
	if opts.Background || opts.AgentContinuation || opts.RetryInput || opts.Discussion != nil || opts.TransientHistory {
		return false
	}
	_, ok := p.Brain.(brain.ContinuityTransferProvider)
	return ok && !brain.ParksSession(p.Brain) && !brain.HostedSharedHistory(p.Brain)
}

// detachableRequest is the context of a movable request. The turn's context
// stops it until the move detaches the two.
func (p *Pipeline) detachableRequest(parent context.Context, input string, opts brain.StreamOptions, started time.Time) (context.Context, context.CancelFunc, *movableRequest) {
	ctx, cancelCause := context.WithCancelCause(context.WithoutCancel(parent))
	stop := context.AfterFunc(parent, func() { cancelCause(context.Cause(parent)) })
	cancel := func() {
		stop()
		cancelCause(context.Canceled)
	}
	before := p.Brain.(brain.ContinuityTransferProvider).ContinuitySnapshot()
	return ctx, cancel, &movableRequest{detach: stop, input: input, opts: opts, started: started, before: before}
}

// movedToolHooks sends tool runs to the job's row once the request is moved.
func (r *transcriptRecording) movedToolHooks(start func(brain.ToolCall), end func(brain.ToolResult)) (func(brain.ToolCall), func(brain.ToolResult)) {
	hooks := func() (func(brain.ToolCall), func(brain.ToolResult)) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.moved {
			return start, end
		}
		if r.movable.activity != nil {
			r.movable.activity()
		}
		return r.movable.toolStart, r.movable.toolEnd
	}
	movedStart := func(call brain.ToolCall) {
		if hook, _ := hooks(); hook != nil {
			hook(call)
		}
	}
	movedEnd := func(result brain.ToolResult) {
		if _, hook := hooks(); hook != nil {
			hook(result)
		}
	}
	return movedStart, movedEnd
}

func (r *transcriptRecording) deliveredText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered.String()
}

// moveToJob hands a request whose turn stopped reading it to a background job
// of the persona that started it. It returns the part of the reply that stays
// with the turn and the job's id, or false when the request cannot be moved;
// the turn then stops it as before.
func (p *Pipeline) moveToJob(ctx context.Context, metrics *turnMetrics, rec *transcriptRecording, stream *brain.Stream, task string) (string, string, bool) {
	a := p.Agency
	if a == nil || rec == nil || rec.movable == nil || !rec.movable.detach() {
		return "", "", false
	}
	partial, carried := splitMovedReply(rec.deliveredText(), metrics.movedSpoken)
	cfg := a.currentConfig()
	admission := frozenConfig(cfg)
	if admission == nil {
		admission = map[string]string{}
	}
	provider, model := admission["provider"], admission["model"]
	if cfg != nil && cfg.BrainProvider != "" {
		provider, model = cfg.BrainProvider, config.BrainModelFor(cfg, cfg.BrainProvider)
	}
	admission["provider"], admission["model"], admission["work"] = provider, model, config.WorkOwn
	workDir := brain.ReplyWorkDir(p.Brain)
	if workDir == "" {
		workDir = config.WorkDirFrom(cfg)
	}
	origin := workerOrigin{Scope: p.ConversationScope(), Trace: metrics.trace, Envelope: metrics.response, admission: admission, continued: true}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = p.responseTTS(ctx)
	origin.releaseVoice = p.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	handOff := a.movedHandOff(context.WithoutCancel(ctx), metrics, task, rec)
	id, err := a.jobs.Start(ctx, job.Spec{
		Task:     truncateJobTask(task),
		Provider: provider,
		Model:    model,
		// The reply keeps the idle limit it had in the foreground instead;
		// work it hands off gets a spawn's limits.
		RuntimeBudget: -1,
		Immediate:     true,
		OnAccepted: func(id string) {
			a.mu.Lock()
			a.workerOrigins[id] = origin
			a.mu.Unlock()
			start, end := a.foldingToolHooks(id)
			p.markMoved(rec, id, partial, workDir, start, end)
		},
		Run: p.drainMoved(rec, stream, carried, handOff),
	})
	if err != nil {
		origin.releaseVoice()
		p.emit(events.Error{Stage: "job", Message: err.Error()})
		return "", "", false
	}
	return partial, id, true
}

// splitMovedReply divides the text a moved turn had read at a sentence end,
// so the job's reply starts with a sentence and nothing is said twice or
// lost: the turn keeps the sentences it had finished saying, or, when it was
// not speaking, every complete sentence it had read. The rest, from the
// whitespace after the last kept sentence, leads the job's reply.
func splitMovedReply(read string, spoken *int) (kept, carried string) {
	ends := brain.SentenceEnds(read)
	n := len(ends)
	if spoken != nil {
		n = min(n, *spoken)
	}
	if n == 0 {
		return "", read
	}
	return read[:ends[n-1]], read[ends[n-1]:]
}

func (p *Pipeline) markMoved(rec *transcriptRecording, jobID, partial, workDir string, start func(brain.ToolCall), end func(brain.ToolResult)) {
	m := rec.movable
	added := requestTurns(m.input, m.opts, partial, nil, m.started)
	history := m.before
	working := history.Working
	if working == nil {
		working = history.Canonical
	}
	history.Canonical = append(brain.CloneTurns(history.Canonical), added...)
	history.Working = append(brain.CloneTurns(working), brain.CloneTurns(added)...)
	history.Archive = slices.Clone(history.Archive)
	rec.mu.Lock()
	rec.moved = true
	m.jobID, m.history, m.workDir, m.toolStart, m.toolEnd = jobID, history, workDir, start, end
	rec.mu.Unlock()
	p.moves.mu.Lock()
	p.moves.moved = append(p.moves.moved, rec)
	p.moves.mu.Unlock()
}

// movedHandOff runs, in the moved reply's job, the work the reply handed off
// when it ended: a spawn admitted from the turn and worker configuration of
// the persona that started it, not the persona answering by then.
func (a *Agency) movedHandOff(ctx context.Context, metrics *turnMetrics, task string, rec *transcriptRecording) func(context.Context, config.WorkerTierName) (string, error) {
	source := a.workerSource()
	started := &turnMetrics{response: metrics.response, trace: metrics.trace}
	return func(jobCtx context.Context, tier config.WorkerTierName) (string, error) {
		w, err := a.admitSpawn(ctx, tier, source)
		if err != nil {
			return "", err
		}
		rec.mu.Lock()
		history := brain.CloneTurns(rec.movable.history.Canonical)
		rec.mu.Unlock()
		if spawnLacksTask(history, strings.TrimSpace(task)) {
			return "", errors.New("the reply handed work off without saying what the work is; ask again")
		}
		run, budget := a.spawnRun(ctx, started, task, task, history, rec.provider, w)
		id := job.IDFromContext(jobCtx)
		a.mu.Lock()
		origin := a.workerOrigins[id]
		origin.admission = w.admission()
		a.workerOrigins[id] = origin
		a.mu.Unlock()
		if w.own {
			a.noteOwnWorkDefault(w.decision)
		}
		if budget == 0 {
			budget = a.jobs.Budget()
		}
		if budget > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeoutCause(jobCtx, budget, fmt.Errorf("%w after %s", job.ErrBudget, budget))
			defer cancel()
		}
		return run(jobCtx)
	}
}

// idleWatch stops a moved request that has been silent for as long as the
// live turn would have waited: no text, thinking or tool activity.
type idleWatch struct {
	mu    sync.Mutex
	timer *time.Timer
	limit time.Duration
	fired bool
}

func (p *Pipeline) watchMovedIdle(rec *transcriptRecording) *idleWatch {
	limit, ok := p.brainTimeout()
	if !ok {
		return nil
	}
	w := &idleWatch{limit: limit}
	w.timer = time.AfterFunc(limit, func() {
		w.mu.Lock()
		w.fired = true
		w.mu.Unlock()
		rec.cancel()
	})
	rec.mu.Lock()
	rec.movable.activity = w.touch
	rec.mu.Unlock()
	return w
}

func (w *idleWatch) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.fired && w.timer.Stop() {
		w.timer.Reset(w.limit)
	}
}

// stop ends the watch and reports whether it had stopped the request.
func (w *idleWatch) stop() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer.Stop()
	return w.fired
}

// drainMoved is a moved request's job: it reads the rest of the stream and
// returns the text the turn had not read, or runs the work the reply handed
// off.
func (p *Pipeline) drainMoved(rec *transcriptRecording, stream *brain.Stream, carried string, handOff func(context.Context, config.WorkerTierName) (string, error)) func(context.Context) (string, error) {
	return func(jobCtx context.Context) (string, error) {
		idle := p.watchMovedIdle(rec)
		var rest strings.Builder
		chunks, thinking := stream.Chunks, stream.Thinking
		for chunks != nil || thinking != nil {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				idle.touch()
				rest.WriteString(chunk)
			case _, ok := <-thinking:
				if !ok {
					thinking = nil
					continue
				}
				idle.touch()
			}
		}
		result := <-stream.Done
		stalled := idle.stop()
		if err := jobCtx.Err(); err != nil {
			return "", err
		}
		if stalled {
			return "", fmt.Errorf("the model sent nothing for %s, so the reply was stopped", idle.limit)
		}
		if result.Err != nil && !result.Recovered {
			return "", result.Err
		}
		switch kind := routeKindFromBrain(result.Route); {
		case kind == RouteSpawn:
			return handOff(jobCtx, result.RouteTier)
		case routeKindSkipsBrain(kind):
			return "", fmt.Errorf("the reply asked to %s, which only works in the live conversation; ask again", result.Route)
		}
		return p.finishMovedText(jobCtx, rec, carried+rest.String())
	}
}

// finishMovedText gives a moved reply what a worker's answer gets before it
// is shown, spoken and saved: thinking removed, and image lines imported as
// the job's attachments instead of being left as paths.
func (p *Pipeline) finishMovedText(jobCtx context.Context, rec *transcriptRecording, raw string) (string, error) {
	text := textclean.StripThinkBlocks(raw)
	if !strings.Contains(text, "![") {
		return text, nil
	}
	rec.mu.Lock()
	workDir := rec.movable.workDir
	rec.mu.Unlock()
	cleaned, refs, err := chatmedia.DefaultStore().ImportReply(text, workDir)
	if err != nil {
		p.emit(events.Info{Message: err.Error()})
	}
	if len(refs) == 0 {
		if err != nil && chatmedia.ReplyProse(text) == "" {
			return "", fmt.Errorf("could not attach the reply's images: %w", err)
		}
		return cleaned, nil
	}
	if p.Agency != nil {
		p.Agency.saveWorkerThread(job.IDFromContext(jobCtx), []brain.Turn{{Role: "assistant", Content: cleaned, Attachments: refs}})
	}
	return cleaned, nil
}

// finishMovedTurn ends a turn whose reply continues in job id. The part it
// kept is given as that reply's beginning.
func (p *Pipeline) finishMovedTurn(turn *turnConductor, metrics *turnMetrics, partial, id string) {
	p.emitResponse(metrics, events.ResponseReady{Response: brain.CleanForVoice(partial), ContinuedAs: id})
	turn.finish(TurnMoved)
}

// MovedContinuity is the conversation as the host reads it from provider
// while a request moved off it may still be running there: as it stood when
// that request started, with the request's user line and the reply its turn
// had shown. ok is false when provider has no moved request.
func (p *Pipeline) MovedContinuity(provider brain.Provider) (brain.ContinuitySnapshot, bool) {
	for _, rec := range p.movedOn(provider) {
		rec.mu.Lock()
		history := rec.movable.history
		rec.mu.Unlock()
		history.Canonical = brain.CloneTurns(history.Canonical)
		history.Working = brain.CloneTurns(history.Working)
		history.Archive = slices.Clone(history.Archive)
		return history, true
	}
	return brain.ContinuitySnapshot{}, false
}

func (p *Pipeline) movedOn(provider brain.Provider) []*transcriptRecording {
	if provider == nil {
		return nil
	}
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	var out []*transcriptRecording
	for _, rec := range p.moves.moved {
		if rec.provider == provider {
			out = append(out, rec)
		}
	}
	return out
}

func (p *Pipeline) forgetMoved(rec *transcriptRecording) {
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	p.moves.moved = slices.DeleteFunc(p.moves.moved, func(r *transcriptRecording) bool { return r == rec })
}

// liveBrainBusy reports whether the live provider still has a request moved
// off it; nothing but its replacement may use it until then.
func (p *Pipeline) liveBrainBusy() bool { return len(p.movedOn(p.Brain)) > 0 }

// reclaimMovedBrain is called before the live provider serves a request,
// history change or read of its own. A request moved off it means the turn
// that followed did not install another provider, so the moved request is
// stopped as the turn would have stopped it: two requests never share one
// provider.
func (p *Pipeline) reclaimMovedBrain() {
	for _, rec := range p.movedOn(p.Brain) {
		rec.mu.Lock()
		m := rec.movable
		jobID := m.jobID
		rec.moved = false
		rec.mu.Unlock()
		if p.Agency != nil {
			p.Agency.jobs.Abort(jobID)
		}
		rec.release()
		if p.Agency != nil && p.Agency.jobState(jobID) == job.StateDone {
			// The job finished first and delivers the text it read; the
			// provider recorded the whole reply itself. Keep the record the
			// job's reply extends instead.
			<-rec.drained
			if target, ok := p.Brain.(brain.ContinuityTransferProvider); ok {
				if err := target.ImportContinuity(m.history); err != nil {
					p.emit(events.Error{Stage: "history", Message: fmt.Sprintf("restore history after a moved reply: %v", err)})
				}
			}
		}
		p.forgetMoved(rec)
	}
}

// awaitMoved waits until provider's moved requests have ended. Each ends on
// its own, by its job being stopped, or when the conversation closes.
func (p *Pipeline) awaitMoved(provider brain.Provider) {
	for _, rec := range p.movedOn(provider) {
		<-rec.drained
		p.forgetMoved(rec)
	}
}

// stopMovedRequest stops the request moved to job id once the job has ended,
// however it ended: a job stopped before it ran never read its stream.
func (p *Pipeline) stopMovedRequest(id string) {
	p.moves.mu.Lock()
	moved := slices.Clone(p.moves.moved)
	p.moves.mu.Unlock()
	for _, rec := range moved {
		rec.mu.Lock()
		owned := rec.movable.jobID == id
		rec.mu.Unlock()
		if owned {
			rec.cancel()
		}
	}
}

// stopMoved stops every moved request, as closing the conversation does.
func (p *Pipeline) stopMoved() {
	p.moves.mu.Lock()
	moved := slices.Clone(p.moves.moved)
	p.moves.mu.Unlock()
	for _, rec := range moved {
		rec.mu.Lock()
		jobID := rec.movable.jobID
		rec.mu.Unlock()
		if p.Agency != nil {
			p.Agency.jobs.Abort(jobID)
		}
		rec.cancel()
	}
}
