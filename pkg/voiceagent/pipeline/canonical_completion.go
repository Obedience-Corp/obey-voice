package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func (p *Pipeline) runCanonicalCompletion(ctx context.Context, lease work.CompletionLease, selected []work.ResultView) error {
	p.incActiveTurn()
	defer p.decActiveTurn()
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	metrics.response = events.ResponseFromContext(ctx)
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics, ctx)
	ctx = events.WithTrace(ctx, metrics.trace)
	turn.to(TurnThinking)
	p.emitResponse(metrics, events.ThinkingStarted{})
	c, provider := p.WorkCoordinator(), p.Brain
	if p.completionScope != lease.Scope {
		p.completionScope = lease.Scope
		p.completionFallback = make(map[string]bool)
	}

	tasks := make(map[string]string)
	for _, j := range c.Snapshot(lease.Scope).Jobs {
		tasks[j.Key.JobID] = j.Task
	}
	var projections []brain.CompletionResult
	forceBrief := false
	for _, r := range selected {
		o := r.Outcome
		origin := o.Config["persona"]
		if origin == "" {
			origin = o.Origin
		}
		projections = append(projections, brain.CompletionResult{ResultID: o.ResultID, JobID: o.JobID, Task: tasks[o.JobID], Origin: origin, Status: string(o.Status), Brief: o.Text, SourceRef: o.SourceRef, Limitations: o.Limitations, DetailRef: o.ResultID, ObservedAt: o.EndedAt, Revision: o.TerminalRevision})
		forceBrief = forceBrief || p.completionFallback[o.ResultID]
	}
	req := brain.BuildCompletionRequest("", "", provider.History(), projections)
	req.OnUsage = p.usageHook()
	ids := make([]string, 0, len(req.Results))
	for _, r := range req.Results {
		ids = append(ids, r.ResultID)
	}
	p.emitCompletion(lease, "result.projected", ids, "", "", 0)
	p.emitCompletion(lease, "announcement.scheduled", ids, "", "", 0)
	if len(req.Results) == 0 {
		_ = c.HoldAttempt(lease, "result_context_unavailable")
		turn.finish(TurnCompleted)
		return nil
	}
	var envelope brain.CompletionEnvelope
	fallbackReason := ""
	completion, supported := provider.(brain.CompletionProvider)
	if forceBrief {
		fallbackReason = "previous_coverage_omitted"
	} else if !supported {
		fallbackReason = "provider_constraints_unavailable"
	} else {
		generationCtx, cancel := context.WithTimeout(ctx, brain.CompletionDeadline)
		var err error
		metrics.modelRequest = time.Now()
		p.traceSpan(metrics.trace, traceID(metrics.trace, "model"), "model", "start", "running", metrics.modelRequest, time.Time{}, nil, nil)
		envelope, err = completion.Complete(generationCtx, req)
		metrics.modelComplete = time.Now()
		metrics.modelOutcome = "completed"
		if err != nil {
			metrics.modelOutcome = "failed"
		}
		deadlineErr := generationCtx.Err()
		cancel()
		if ctx.Err() != nil || !lease.Valid() {
			turn.finish(TurnInterrupted)
			return context.Canceled
		}
		if err != nil || deadlineErr != nil {
			fallbackReason = "generation_failed"
		} else if envelope.Validate(req) != nil {
			fallbackReason = "invalid_coverage"
		}
	}
	if fallbackReason != "" {
		envelope = deterministicCompletion(req)
		p.emitCompletion(lease, "announcement.fallback", ids, fallbackReason, "", 0)
	}
	addressed := make([]string, 0, len(envelope.Updates))
	covered := make(map[string]bool)
	for _, u := range envelope.Updates {
		addressed = append(addressed, u.ResultID)
		covered[u.ResultID] = true
	}
	for _, r := range selected {
		if !covered[r.Outcome.ResultID] {
			p.completionFallback[r.Outcome.ResultID] = true
		}
	}
	p.emitCompletion(lease, "announcement.addressed", addressed, "", "", 0)
	if len(addressed) == 0 {
		// A syntactically valid answer that omitted the actual result does not
		// announce it. One brief is eligible at the next safe boundary.
		turn.finish(TurnCompleted)
		return nil
	}
	if ctx.Err() != nil || !lease.Valid() {
		turn.finish(TurnInterrupted)
		return context.Canceled
	}
	voice := p.completionNeedsVoice()
	if voice {
		metrics.delivery = &completionPlayback{lease: lease}
	}
	responseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	response, interrupted, err := p.streamResponse(responseCtx, cancel, completionTextStream(envelope.Render()), true, metrics, turn, nil)
	if interrupted {
		p.keepCapture.Store(true)
	}
	if interrupted || errors.Is(err, context.Canceled) || ctx.Err() != nil || !lease.Valid() {
		p.emitCompletion(lease, "audio.delivery", addressed, "interrupted", "voice", 0)
		turn.finish(TurnInterrupted)
		return context.Canceled
	}
	committed := false
	if strings.TrimSpace(response) != "" {
		if appendErr := appendResponse(provider, response, metrics.responseIdentity()); appendErr == nil {
			committed = true
		} else {
			p.emit(events.Error{Stage: "completion", Message: fmt.Sprintf("record background reply: %v", appendErr)})
		}
	}
	mode := "text"
	receipt := work.DeliveryReceipt{Scope: lease.Scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: mode, ResponseCommitted: committed}
	reason := ""
	if !committed {
		reason = "history_unavailable"
	}
	if voice {
		mode = "voice"
		receipt.Mode = mode
		receipt.Segments = metrics.delivery.finished
		receipt.Audible = p.sinkKind() == audio.SinkLocalSpeaker
		receipt.PlaybackComplete = metrics.delivery.complete() && err == nil
		if !receipt.PlaybackComplete || !receipt.Audible {
			reason = "playback_failed"
		}
		if p.OutputMuted() {
			reason = "audio_muted"
		} else if p.Player == nil || !p.ttsReady() || p.sinkKind() == audio.SinkNone {
			reason = "audio_unavailable"
		}
		if p.sinkKind() == audio.SinkStreamClient {
			reason = "remote_playback_unconfirmed"
		}
		p.emitCompletion(lease, "audio.delivery", addressed, reason, mode, receipt.Segments)
	}
	p.emitResponse(metrics, events.ResponseReady{Response: response, Degraded: reason != "" || err != nil})
	if reason != "" {
		if holdErr := c.HoldAttempt(lease, reason); holdErr == nil {
			p.emitCompletion(lease, "announcement.held", ids, reason, mode, receipt.Segments)
		}
	} else if finishErr := c.FinishAttempt(lease, addressed, receipt); finishErr != nil {
		turn.finish(TurnInterrupted)
		return finishErr
	} else {
		for _, id := range addressed {
			delete(p.completionFallback, id)
		}
		p.emitCompletion(lease, "announcement.communicated", addressed, "", mode, receipt.Segments)
	}
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return err
}

func (p *Pipeline) completionNeedsVoice() bool {
	primary, fallback := p.ttsProviders()
	return p.STT != nil || p.Player != nil || primary != nil || fallback != nil
}

// Unsupported providers use canonical data directly. No tool/model can turn
// these result records into instructions; generation has no retry loop.
func deterministicCompletion(req brain.CompletionRequest) brain.CompletionEnvelope {
	var env brain.CompletionEnvelope
	for _, r := range req.Results {
		title := strings.TrimSpace(r.Task)
		if title == "" {
			title = "The background task"
		}
		status := " is back: "
		switch r.Status {
		case "failed", "timed_out", "cancelled", "interrupted":
			status = " " + strings.ReplaceAll(r.Status, "_", " ") + ": "
		case "partial":
			status = " returned a partial result: "
		}
		brief := strings.TrimSpace(r.Brief)
		if brief == "" {
			brief = "No readable outcome was returned."
		}
		text := title + status + brief
		if r.SourceRef != "" {
			text += " Source: " + r.SourceRef + "."
		}
		if r.Limitations != "" {
			text += " Limitation: " + r.Limitations + "."
		}
		if r.Truncated {
			text += " This update is shortened; full details remain in the result card."
		}
		env.Updates = append(env.Updates, brain.CompletionUpdate{ResultID: r.ResultID, Text: text})
	}
	return env
}

func completionTextStream(text string) *brain.Stream {
	chunks := make(chan string, 1)
	chunks <- text
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}
}

func (p *Pipeline) emitCompletion(l work.CompletionLease, stage string, ids []string, reason, mode string, segments int) {
	p.emit(events.WorkDelivery{Scope: l.Scope, AttemptID: l.AttemptID, TurnID: l.TurnID, CaptureEpoch: l.CaptureEpoch, Stage: stage, ResultIDs: append([]string(nil), ids...), Reason: reason, Mode: mode, Segments: segments})
}

// RecoverCompletionOutput is called on an explicit output recovery/unmute or
// user action, never by the scheduler's periodic recovery tick.
func (p *Pipeline) RecoverCompletionOutput() {
	if p.OutputMuted() {
		return
	}
	if c := p.WorkCoordinator(); c != nil {
		for _, r := range c.Pending(c.Scope()) {
			if r.State != work.Held {
				continue
			}
			switch r.HoldReason {
			case "audio_muted", "audio_unavailable", "playback_failed", "tts_failed", "remote_playback_unconfirmed":
				_ = c.Resume(c.Scope(), r.Outcome.ResultID)
			}
		}
	}
}
