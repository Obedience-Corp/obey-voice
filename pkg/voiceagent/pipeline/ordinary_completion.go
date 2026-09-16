package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type ordinaryCompletion struct {
	lease        work.CompletionLease
	request      brain.CompletionRequest
	fallback     string
	fallbackSent atomic.Bool
	wrapperDone  chan struct{}
	coordinator  *work.Coordinator
	provider     brain.Provider
	cancel       context.CancelFunc
	stop, joined chan struct{}
	ctx          context.Context
}

func (p *Pipeline) beginOrdinaryCompletion(ctx context.Context, input, speaker string) (context.Context, *ordinaryCompletion, error) {
	p.RecoverCompletionOutput()
	c := p.WorkCoordinator()
	if c == nil || p.OutputMuted() || p.HasCapturedInput() {
		return ctx, nil, nil
	}
	p.captureMu.Lock()
	if p.captureSession != nil {
		p.captureMu.Unlock()
		return ctx, nil, nil
	}
	p.captureMu.Unlock()
	scope := c.Scope()
	var selected []work.ResultView
	var refs []work.ResultRef
	for _, r := range c.Pending(scope) {
		if r.State != work.Pending && r.State != work.Ready {
			continue
		}
		selected = append(selected, r)
		refs = append(refs, work.ResultRef{Scope: scope, JobID: r.Outcome.JobID, ResultID: r.Outcome.ResultID, Revision: r.Outcome.TerminalRevision})
		if len(refs) == brain.CompletionMaxResults {
			break
		}
	}
	if len(refs) == 0 {
		return ctx, nil, nil
	}
	id := fmt.Sprintf("ordinary-completion-%d", p.captureRequests.Add(1))
	var permit work.CapturePermit
	var err error
	if p.externalCompletionCapture != nil {
		permit, err = p.externalCompletionCapture(ctx, c, id)
	} else {
		permit, err = c.AcquireCapturePermit(scope, id, p.captureRequests.Load())
	}
	if err != nil {
		return ctx, nil, err
	}
	lease, err := c.Claim(scope, id, id, refs, permit)
	if err != nil {
		_ = c.ReleaseCapturePermit(permit)
		return ctx, nil, err
	}
	projections := make([]brain.CompletionResult, 0, len(selected))
	for _, r := range selected {
		o := r.Outcome
		origin := o.Config["persona"]
		if origin == "" {
			origin = o.Origin
		}
		projections = append(projections, brain.CompletionResult{ResultID: o.ResultID, JobID: o.JobID, Task: r.Task, Status: string(o.Status), Brief: o.Text, Origin: origin, SourceRef: o.SourceRef, Limitations: o.Limitations, DetailRef: o.ResultID, ObservedAt: o.EndedAt, Revision: o.TerminalRevision})
	}
	req := brain.BuildCompletionRequest(input, speaker, p.Brain.History(), projections)
	ids := make([]string, 0, len(req.Results))
	for _, r := range req.Results {
		ids = append(ids, r.ResultID)
	}
	p.emitCompletion(lease, "result.projected", ids, "", "", 0)
	p.emitCompletion(lease, "announcement.scheduled", ids, "", "", 0)
	o := &ordinaryCompletion{lease: lease, request: req, fallback: deterministicCompletion(req).Render(), coordinator: c, provider: p.Brain}
	attempt, cancel := context.WithCancel(ctx)
	o.cancel = cancel
	o.ctx = attempt
	o.stop, o.joined = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(o.joined)
		select {
		case <-lease.Done():
			cancel()
		case <-attempt.Done():
			_ = c.RevokeCompletionLease(lease)
		case <-o.stop:
		}
	}()
	return audio.WithPlaybackAttempt(attempt, audio.PlaybackAttempt{ID: lease.AttemptID, CaptureEpoch: lease.CaptureEpoch, Fence: lease, Render: &audio.FirstRenderBoundary{}}), o, nil
}
func (o *ordinaryCompletion) close() {
	if o == nil {
		return
	}
	o.cancel()
	close(o.stop)
	<-o.joined
	if o.wrapperDone != nil {
		<-o.wrapperDone
	}
	_ = o.coordinator.ReleaseCompletionLease(o.lease)
}
func (o *ordinaryCompletion) wrap(s *brain.Stream) *brain.Stream {
	out, thinking, done := make(chan string, 8), make(chan string, 8), make(chan brain.StreamResult, 1)
	o.wrapperDone = make(chan struct{})
	go func() {
		defer close(o.wrapperDone)
		defer close(out)
		defer close(thinking)
		defer close(done)
		chunks, thinks, terminal := s.Chunks, s.Thinking, s.Done
		var result brain.StreamResult
		received, had := false, false
		for chunks != nil || !received {
			select {
			case <-o.ctx.Done():
				return
			case v, ok := <-thinks:
				if !ok {
					thinks = nil
					continue
				}
				select {
				case thinking <- v:
				case <-o.ctx.Done():
					return
				}
			case v, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if strings.TrimSpace(v) != "" {
					had = true
				}
				select {
				case out <- v:
				case <-o.ctx.Done():
					return
				}
			case v, ok := <-terminal:
				if !ok && !received {
					result.Err = fmt.Errorf("ordinary completion: missing provider result")
				} else if ok {
					result = v
				}
				received = true
				terminal = nil
			}
		}
		if result.Err == nil && !result.Recovered && result.Route == "" && had && strings.TrimSpace(o.fallback) != "" && o.lease.Valid() {
			select {
			case out <- "\n" + o.fallback:
				o.fallbackSent.Store(true)
			case <-o.ctx.Done():
				return
			}
		}
		done <- result
	}()
	return &brain.Stream{Chunks: out, Thinking: thinking, Done: done}
}

func (o *ordinaryCompletion) commitFallback(response string) error {
	if !o.fallbackSent.Load() || strings.TrimSpace(response) == "" {
		return fmt.Errorf("ordinary completion: no update was rendered")
	}
	metrics := &turnMetrics{response: events.ResponseFromContext(o.ctx)}
	return appendResponse(o.provider, o.fallback, metrics.responseIdentity())
}

func (p *Pipeline) finishOrdinaryCompletion(metrics *turnMetrics, response string, interrupted bool) {
	o := metrics.ordinary
	if o == nil || !o.fallbackSent.Load() || interrupted || o.ctx.Err() != nil || !o.lease.Valid() {
		return
	}
	ids := make([]string, 0, len(o.request.Results))
	for _, r := range o.request.Results {
		ids = append(ids, r.ResultID)
	}
	if len(ids) == 0 {
		return
	}
	p.emitCompletion(o.lease, "announcement.addressed", ids, "", "", 0)
	reason := ""
	committed := o.commitFallback(response) == nil
	if !committed {
		reason = "history_unavailable"
	}
	receipt := work.DeliveryReceipt{Scope: o.lease.Scope, AttemptID: o.lease.AttemptID, TurnID: o.lease.TurnID, CaptureEpoch: o.lease.CaptureEpoch, Mode: "text", ResponseCommitted: committed}
	if p.completionNeedsVoice() {
		receipt.Mode = "voice"
		if metrics.delivery != nil {
			receipt.Segments = metrics.delivery.finished
			receipt.PlaybackComplete = metrics.delivery.complete()
			receipt.Audible = p.sinkKind() == audio.SinkLocalSpeaker
		}
		if !receipt.PlaybackComplete || !receipt.Audible {
			reason = "playback_failed"
		}
		if p.OutputMuted() {
			reason = "audio_muted"
		} else if p.sinkKind() == audio.SinkNone {
			reason = "audio_unavailable"
		} else if p.sinkKind() == audio.SinkStreamClient {
			reason = "remote_playback_unconfirmed"
		}
		p.emitCompletion(o.lease, "audio.delivery", ids, reason, receipt.Mode, receipt.Segments)
	}
	if reason != "" {
		if err := o.coordinator.HoldAttempt(o.lease, reason); err == nil {
			p.emitCompletion(o.lease, "announcement.held", ids, reason, receipt.Mode, receipt.Segments)
		}
	} else if err := o.coordinator.FinishAttempt(o.lease, ids, receipt); err == nil {
		p.emitCompletion(o.lease, "announcement.communicated", ids, "", receipt.Mode, receipt.Segments)
	}
}
