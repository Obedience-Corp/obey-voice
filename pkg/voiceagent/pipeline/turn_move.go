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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
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
	toolStart func(brain.ToolCall)
	toolEnd   func(brain.ToolResult)
}

// turnMove is a running turn's offer to be moved; requested closes when
// MoveTurnToBackground takes it.
type turnMove struct {
	requested chan struct{}
	taken     bool
}

type turnMoves struct {
	mu    sync.Mutex
	offer *turnMove
	// moved are requests owned by background jobs whose provider has not
	// been retired yet.
	moved []*transcriptRecording
}

// MoveTurnToBackground asks the running turn to hand its model request to a
// background job instead of ending it, because input names another persona.
// It reports whether the turn accepted; when it did not, the caller stops the
// turn as before. A turn accepts only while it reads its model's reply on a
// provider whose request can be moved.
func (p *Pipeline) MoveTurnToBackground(input string) bool {
	p.moves.mu.Lock()
	offer := p.moves.offer
	p.moves.mu.Unlock()
	if offer == nil || p.NamesOtherPersona == nil || p.Agency == nil || p.Agency.jobs.CheckAdmission() != nil {
		return false
	}
	if !p.NamesOtherPersona(input) {
		return false
	}
	p.moves.mu.Lock()
	defer p.moves.mu.Unlock()
	if p.moves.offer != offer || offer.taken {
		return false
	}
	offer.taken = true
	close(offer.requested)
	return true
}

// offerMove lets MoveTurnToBackground reach a turn while it reads rec's
// stream. The returned func withdraws the offer.
func (p *Pipeline) offerMove(rec *transcriptRecording) (<-chan struct{}, func()) {
	if rec == nil || rec.movable == nil {
		return nil, func() {}
	}
	offer := &turnMove{requested: make(chan struct{})}
	p.moves.mu.Lock()
	p.moves.offer = offer
	p.moves.mu.Unlock()
	return offer.requested, func() {
		p.moves.mu.Lock()
		if p.moves.offer == offer {
			p.moves.offer = nil
		}
		p.moves.mu.Unlock()
	}
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
		if r.moved {
			return r.movable.toolStart, r.movable.toolEnd
		}
		return start, end
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
// of the persona that started it, and returns the text the turn had read. It
// reports false when the request cannot be moved; the turn then stops it as
// before.
func (p *Pipeline) moveToJob(ctx context.Context, metrics *turnMetrics, rec *transcriptRecording, stream *brain.Stream, task string) (string, bool) {
	a := p.Agency
	if a == nil || rec == nil || rec.movable == nil || !rec.movable.detach() {
		return "", false
	}
	partial := rec.deliveredText()
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
	origin := workerOrigin{Scope: p.ConversationScope(), Trace: metrics.trace, Envelope: metrics.response, admission: admission}
	origin.Envelope.ResponseID = newWorkID()
	origin.Voices.primary, origin.Voices.fallback = p.responseTTS(ctx)
	origin.releaseVoice = p.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	_, err := a.jobs.Start(ctx, job.Spec{
		Task:      truncateJobTask(task),
		Provider:  provider,
		Model:     model,
		Immediate: true,
		OnAccepted: func(id string) {
			a.mu.Lock()
			a.workerOrigins[id] = origin
			a.mu.Unlock()
			start, end := a.foldingToolHooks(id)
			p.markMoved(rec, id, partial, start, end)
		},
		Run: rec.drainMoved(stream),
	})
	if err != nil {
		origin.releaseVoice()
		p.emit(events.Error{Stage: "job", Message: err.Error()})
		return "", false
	}
	return partial, true
}

func (p *Pipeline) markMoved(rec *transcriptRecording, jobID, partial string, start func(brain.ToolCall), end func(brain.ToolResult)) {
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
	m.jobID, m.history, m.toolStart, m.toolEnd = jobID, history, start, end
	rec.mu.Unlock()
	p.moves.mu.Lock()
	p.moves.moved = append(p.moves.moved, rec)
	p.moves.mu.Unlock()
}

// drainMoved is a moved request's job: it reads the rest of the stream and
// returns the text the turn had not read.
func (r *transcriptRecording) drainMoved(stream *brain.Stream) func(context.Context) (string, error) {
	return func(jobCtx context.Context) (string, error) {
		stop := context.AfterFunc(jobCtx, r.cancel)
		defer stop()
		var rest strings.Builder
		chunks, thinking := stream.Chunks, stream.Thinking
		for chunks != nil || thinking != nil {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				rest.WriteString(chunk)
			case _, ok := <-thinking:
				if !ok {
					thinking = nil
				}
			}
		}
		result := <-stream.Done
		if err := jobCtx.Err(); err != nil {
			return "", err
		}
		if result.Err != nil && !result.Recovered {
			return "", result.Err
		}
		if routeKindSkipsBrain(routeKindFromBrain(result.Route)) {
			return "", fmt.Errorf("the reply asked for %q, which a reply finished in the background cannot do", result.Route)
		}
		return strings.TrimSpace(rest.String()), nil
	}
}

// finishMovedTurn ends a turn whose request now belongs to a job. What the
// turn had shown stays shown as a partial reply.
func (p *Pipeline) finishMovedTurn(turn *turnConductor, metrics *turnMetrics, partial string) {
	metrics.interrupted = true
	partial = strings.TrimSpace(brain.CleanForVoice(partial))
	p.emitResponse(metrics, events.ResponseReady{Response: partial, Interrupted: true})
	turn.finish(TurnInterrupted)
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
		rec.settle()
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
