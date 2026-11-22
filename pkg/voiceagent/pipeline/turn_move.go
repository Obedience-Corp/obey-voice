package pipeline

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// errNotTakenOver stops a moved reply when the turn after it left its
// persona's provider in place.
var errNotTakenOver = errors.New("the persona it was moved for could not take over")

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
	stopped chan struct{}
	stop    sync.Once
	// jobEnded closes once the job's end has been followed up.
	jobEnded chan struct{}
	endJob   sync.Once
	// announce orders the job's start of a tool that began before the move
	// ahead of that tool's end.
	announce sync.Mutex

	// Under the recording's mu. open holds tool calls started before the
	// move that had not ended at it.
	open      map[string]brain.ToolCall
	jobID     string
	kept      string
	history   brain.ContinuitySnapshot
	workDir   string
	toolStart func(brain.ToolCall)
	toolEnd   func(brain.ToolResult)
	activity  func()
	// finished is set once the model's reply ended without failing;
	// reclaimed once the provider is to record the request itself.
	finished, reclaimed bool
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
	// jobs maps each job continuing a moved reply to it until the job ends.
	jobs map[string]*transcriptRecording
	// recording joins the records of moved replies that did not finish.
	recording sync.WaitGroup
}

// moveOfferWait bounds how long MoveTurnToBackground waits for a turn that is
// busy outside its stream loop for a moment. The offer is withdrawn as soon as
// the turn can no longer be moved, so this is a safety net.
const moveOfferWait = 200 * time.Millisecond

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
// stream. The returned func withdraws the offer; metrics keeps it so the
// stream loop withdraws it the moment it stops taking requests.
func (p *Pipeline) offerMove(rec *transcriptRecording, metrics *turnMetrics) (<-chan moveRequest, func()) {
	if rec == nil || rec.movable == nil {
		return nil, func() {}
	}
	offer := &turnMove{requests: make(chan moveRequest), withdrawn: make(chan struct{})}
	p.moves.mu.Lock()
	p.moves.offer = offer
	p.moves.mu.Unlock()
	withdraw := sync.OnceFunc(func() {
		p.moves.mu.Lock()
		if p.moves.offer == offer {
			p.moves.offer = nil
		}
		p.moves.mu.Unlock()
		close(offer.withdrawn)
	})
	metrics.withdrawMove = withdraw
	return offer.requests, withdraw
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
	return ctx, cancel, &movableRequest{detach: stop, input: input, opts: opts, started: started, before: before,
		stopped: make(chan struct{}), jobEnded: make(chan struct{}), open: make(map[string]brain.ToolCall)}
}

// stopMove stops a moved request that is not to finish.
func (r *transcriptRecording) stopMove() {
	r.movable.stop.Do(func() { close(r.movable.stopped) })
	r.cancel()
}

// movedToolHooks sends tool runs to the job's row once the request is moved.
// A tool that started before the move is started on the row before its end
// goes there.
func (r *transcriptRecording) movedToolHooks(start func(brain.ToolCall), end func(brain.ToolResult)) (func(brain.ToolCall), func(brain.ToolResult)) {
	m := r.movable
	movedStart := func(call brain.ToolCall) {
		r.mu.Lock()
		if !r.moved {
			m.open[call.ID] = call
			r.mu.Unlock()
			if start != nil {
				start(call)
			}
			return
		}
		activity, hook := m.activity, m.toolStart
		r.mu.Unlock()
		if activity != nil {
			activity()
		}
		if hook != nil {
			hook(call)
		}
	}
	movedEnd := func(result brain.ToolResult) {
		r.mu.Lock()
		if !r.moved {
			delete(m.open, result.ID)
			r.mu.Unlock()
			if end != nil {
				end(result)
			}
			return
		}
		call, open := m.open[result.ID]
		delete(m.open, result.ID)
		activity, startHook, endHook := m.activity, m.toolStart, m.toolEnd
		r.mu.Unlock()
		if activity != nil {
			activity()
		}
		m.announce.Lock()
		defer m.announce.Unlock()
		if open && startHook != nil {
			startHook(call)
		}
		if endHook != nil {
			endHook(result)
		}
	}
	return movedStart, movedEnd
}

// announceOpenTools starts on the job's row the tools still running at the
// move.
func (r *transcriptRecording) announceOpenTools() {
	m := r.movable
	m.announce.Lock()
	defer m.announce.Unlock()
	r.mu.Lock()
	calls := make([]brain.ToolCall, 0, len(m.open))
	for _, call := range m.open {
		calls = append(calls, call)
	}
	clear(m.open)
	hook := m.toolStart
	r.mu.Unlock()
	slices.SortFunc(calls, func(a, b brain.ToolCall) int { return strings.Compare(a.ID, b.ID) })
	for _, call := range calls {
		if hook != nil {
			hook(call)
		}
	}
}

func (r *transcriptRecording) deliveredText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered.String()
}

// moveToJob hands a request whose turn stopped reading it to a background job
// of the persona that started it, and gives the part of the reply the turn
// keeps. It reports false when the request cannot be moved; the turn then
// stops it as before.
func (p *Pipeline) moveToJob(ctx context.Context, metrics *turnMetrics, rec *transcriptRecording, stream *brain.Stream, task string) bool {
	a := p.Agency
	if a == nil || rec == nil || rec.movable == nil || !rec.movable.detach() {
		return false
	}
	kept, carried := splitMovedReply(metrics.movedRead, metrics.movedSpoken)
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
	if kept != "" {
		origin.keptResponseID = metrics.response.ResponseID
	}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = p.responseTTS(ctx)
	origin.releaseVoice = p.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	handOff := a.movedHandOff(context.WithoutCancel(ctx), metrics, task, rec)
	_, err := a.jobs.Start(ctx, job.Spec{
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
			p.markMoved(rec, id, kept, workDir, start, end)
			// Before the job's row, so the part kept reads ahead of it.
			p.emitResponse(metrics, events.ResponseReady{Response: textclean.StripThinkBlocks(kept), ContinuedAs: id})
		},
		Run: p.drainMoved(rec, stream, carried, handOff),
	})
	if err != nil {
		origin.releaseVoice()
		p.emit(events.Error{Stage: "job", Message: err.Error()})
		return false
	}
	rec.announceOpenTools()
	return true
}

// splitMovedReply divides the text a moved turn had read at a sentence end,
// so the job's reply starts with a sentence and nothing is said twice or
// lost: the turn keeps the sentences it had finished saying, or, when it was
// not speaking, every complete sentence it had read. The rest, from the
// whitespace after the last kept sentence, leads the job's reply. The cut
// never falls inside a fenced code block, and an image line goes with the
// job's reply, which attaches it.
func splitMovedReply(read string, spoken *int) (kept, carried string) {
	ends := brain.SentenceEnds(read)
	n := len(ends)
	if spoken != nil {
		n = min(n, *spoken)
	}
	limit := len(read)
	if image := strings.Index(read, "!["); image >= 0 {
		limit = image
	}
	for n > 0 {
		cut := ends[n-1]
		if cut <= limit && strings.Count(read[:cut], "```")%2 == 0 {
			return read[:cut], read[cut:]
		}
		n--
	}
	return "", read
}

func (p *Pipeline) markMoved(rec *transcriptRecording, jobID, kept, workDir string, start func(brain.ToolCall), end func(brain.ToolResult)) {
	m := rec.movable
	// With nothing kept, the request reads as handed off, as a spawned one
	// does, instead of as a line nobody answered.
	shown := kept
	if strings.TrimSpace(brain.CleanForVoice(kept)) == "" {
		shown = spawnAcknowledgement
	}
	added := requestTurns(m.input, m.opts, shown, nil, m.started)
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
	m.jobID, m.kept, m.history, m.workDir, m.toolStart, m.toolEnd = jobID, kept, history, workDir, start, end
	rec.mu.Unlock()
	p.moves.mu.Lock()
	p.moves.moved = append(p.moves.moved, rec)
	if p.moves.jobs == nil {
		p.moves.jobs = make(map[string]*transcriptRecording)
	}
	p.moves.jobs[jobID] = rec
	p.moves.mu.Unlock()
}

// movedHandOff runs, in the moved reply's job, the work the reply handed off
// when it ended: a spawn admitted from the turn and worker configuration of
// the persona that started it, not the persona answering by then. It is held
// to a spawn's runtime budget from when it starts.
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
		a.jobs.Describe(id, providerName(w.cfg), modelName(w.cfg))
		if w.own {
			a.noteOwnWorkDefault(w.decision)
		}
		if budget == 0 {
			budget = a.jobs.Budget()
		}
		a.jobs.Limit(id, budget)
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
		result, ok := <-stream.Done
		if !ok {
			result.Err = errors.New("provider ended without a result")
		}
		stalled := idle.stop()
		if err := jobCtx.Err(); err != nil {
			return "", err
		}
		failure := result.Err
		if stalled {
			failure, result.Recovered = fmt.Errorf("the model sent nothing for %s, so the reply was stopped", idle.limit), false
		}
		id := job.IDFromContext(jobCtx)
		if failure == nil {
			rec.mu.Lock()
			rec.movable.finished = true
			rec.mu.Unlock()
		}
		switch kind := routeKindFromBrain(result.Route); {
		case kind == RouteSpawn && (failure == nil || result.Recovered):
			if failure != nil {
				rec.mu.Lock()
				rec.movable.finished = true
				rec.mu.Unlock()
				p.emit(events.Error{Stage: "brain", Message: id + ": " + failure.Error()})
			}
			return handOff(jobCtx, result.RouteTier)
		case routeKindSkipsBrain(kind) && failure == nil:
			return "", fmt.Errorf("the reply asked to %s, which only works in the live conversation; ask again", result.Route)
		}
		text, err := p.finishMovedText(jobCtx, rec, carried+rest.String())
		if err != nil {
			return "", err
		}
		if failure != nil {
			if strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("the reply ended before the rest of it arrived: %w", failure)
			}
			// Like a live reply that fails partway, the part that arrived is
			// delivered and saved as unfinished, and the reason is reported.
			// A live reply stops speaking at its output limit or a repetition.
			unspoken := errors.Is(failure, brain.ErrResponseLimit) || errors.Is(failure, brain.ErrResponseRepetition)
			p.Agency.noteIncomplete(id, failure, unspoken)
			p.emit(events.Error{Stage: "brain", Message: id + ": the reply was cut short: " + failure.Error()})
			return text, nil
		}
		rec.mu.Lock()
		kept := rec.movable.kept
		rec.mu.Unlock()
		if strings.TrimSpace(text) == "" && kept == "" {
			return "", errors.New("the reply ended without any text")
		}
		return text, nil
	}
}

// noteIncomplete records that job id's reply ended before it was finished.
func (a *Agency) noteIncomplete(id string, reason error, unspoken bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	origin := a.workerOrigins[id]
	origin.incomplete, origin.unspoken = reason, unspoken
	a.workerOrigins[id] = origin
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

func (p *Pipeline) takeMovedJob(id string) *transcriptRecording {
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	rec := p.moves.jobs[id]
	delete(p.moves.jobs, id)
	return rec
}

func (p *Pipeline) forgetMoved(rec *transcriptRecording) {
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	p.moves.moved = slices.DeleteFunc(p.moves.moved, func(r *transcriptRecording) bool { return r == rec })
}

func (p *Pipeline) hasMovedRequests() bool {
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	return len(p.moves.moved) > 0 || len(p.moves.jobs) > 0
}

// liveBrainBusy reports whether the live provider still has a request moved
// off it; nothing but its replacement may use it until then.
func (p *Pipeline) liveBrainBusy() bool { return len(p.movedOn(p.Brain)) > 0 }

// reclaimMovedBrain is called before the live provider serves a request,
// history change or read of its own. A request moved off it means the turn
// that followed did not install another provider, so the moved request is
// stopped as the turn would have stopped it: two requests never share one
// provider. The provider records it itself.
func (p *Pipeline) reclaimMovedBrain() {
	for _, rec := range p.movedOn(p.Brain) {
		rec.mu.Lock()
		m := rec.movable
		jobID := m.jobID
		rec.moved, m.reclaimed = false, true
		rec.mu.Unlock()
		m.stop.Do(func() { close(m.stopped) })
		if p.Agency != nil {
			p.Agency.jobs.Stop(jobID, errNotTakenOver)
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

// awaitMoved waits until provider's moved requests have ended or been told
// to stop. One told to stop is then waited for as any stopped request is
// before its provider is closed.
func (p *Pipeline) awaitMoved(provider brain.Provider) {
	for _, rec := range p.movedOn(provider) {
		select {
		case <-rec.drained:
		case <-rec.movable.stopped:
		}
		p.forgetMoved(rec)
	}
}

// endMovedJob follows job id to its end. A moved reply whose model did not
// finish, however its job ended, is recorded as a stopped live reply is: the
// part kept before the move marked unfinished, and the text that arrived
// after it, once the request has stopped.
func (p *Pipeline) endMovedJob(id string, state job.State, err error) {
	rec := p.takeMovedJob(id)
	if rec == nil {
		return
	}
	rec.stopMove()
	rec.mu.Lock()
	m := rec.movable
	record := !m.finished && !m.reclaimed && state != job.StateDone
	kept := m.kept
	rec.mu.Unlock()
	defer m.endJob.Do(func() { close(m.jobEnded) })
	if !record || p.Agency == nil {
		return
	}
	p.moves.recording.Go(func() {
		settle := time.NewTimer(providerStopTimeout)
		defer settle.Stop()
		select {
		case <-rec.drained:
		case <-settle.C:
		}
		rest, _ := strings.CutPrefix(rec.deliveredText(), kept)
		reason := err
		if reason == nil || (state == job.StateAborted && !errors.Is(err, job.ErrBudget)) {
			reason = context.Canceled
		}
		p.Agency.queueUnfinished(id, textclean.StripThinkBlocks(rest), reason)
	})
}

// stopMoved stops every moved request and records each, as closing the
// conversation does.
func (p *Pipeline) stopMoved() {
	p.moves.mu.Lock()
	moved := slices.Collect(maps.Values(p.moves.jobs))
	p.moves.mu.Unlock()
	for _, rec := range moved {
		rec.mu.Lock()
		jobID := rec.movable.jobID
		rec.mu.Unlock()
		if p.Agency != nil {
			p.Agency.jobs.Abort(jobID)
		}
		rec.stopMove()
	}
	// A job stopped as it starts ends once it has been announced.
	settle := time.NewTimer(providerStopTimeout)
	defer settle.Stop()
	for _, rec := range moved {
		select {
		case <-rec.movable.jobEnded:
		case <-settle.C:
		}
	}
	p.moves.recording.Wait()
}
