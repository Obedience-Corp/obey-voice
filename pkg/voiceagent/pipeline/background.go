package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type backgroundProvider interface {
	BackgroundReady() <-chan struct{}
	BackgroundPending() bool
}

// BackgroundReady exposes a signal for a host-owned serialized turn driver.
// The host must not run a background turn concurrently with a user turn.
func (p *Pipeline) BackgroundReady() <-chan struct{} {
	if c := p.WorkCoordinator(); c != nil {
		return c.Ready()
	}
	if b, ok := p.Brain.(backgroundProvider); ok {
		return b.BackgroundReady()
	}
	return nil
}
func (p *Pipeline) BackgroundPending() bool {
	if p.hasWorkerReplies() || p.heldResultWaiting() {
		return true
	}
	if c := p.WorkCoordinator(); c != nil {
		if p.OutputMuted() {
			return false
		}
		for _, r := range c.Pending(c.Scope()) {
			if (r.State == work.Pending || r.State == work.Ready) && p.deliverableResult(r) {
				return true
			}
		}
		return false
	}
	if b, ok := p.Brain.(backgroundProvider); ok {
		return b.BackgroundPending()
	}
	return false
}

// RunBackgroundTurn first speaks held legacy answers and commits delivered
// replies at the host's idle boundary.
// Other background work uses the ordinary model, speech queue and turn events,
// without emitting a fictional UserInput or granting a new tool-calling turn.
func (p *Pipeline) RunBackgroundTurn(ctx context.Context) error {
	if !p.BackgroundPending() || p.HasCapturedInput() || ctx.Err() != nil {
		return ctx.Err()
	}
	if err := p.awaitProvider(ctx); err != nil {
		return err
	}
	if p.heldResultWaiting() {
		p.Agency.tryFlushPending()
		if !p.hasWorkerReplies() {
			return nil
		}
	}
	if p.hasWorkerReplies() {
		return p.commitWorkerReplies()
	}
	if p.WorkCoordinator() != nil {
		var err error
		// Refresh selection before acquiring the completion lease: replacing a
		// responder revokes the previous binding's capture permission.
		metrics := newTurnMetrics()
		ctx, _, err = p.admitInput(ctx, "", true, metrics)
		if err != nil {
			return err
		}
		return p.withCompletionLease(ctx, func(attemptCtx context.Context, lease work.CompletionLease, selected []work.ResultView) error {
			return p.runCanonicalCompletion(attemptCtx, lease, selected)
		})
	}
	return p.runTextTurn(ctx, "", true)
}

func (p *Pipeline) finishBackgroundStream(ctx context.Context, brainCtx context.Context, cancel context.CancelFunc, stream *brain.Stream, metrics *turnMetrics, turn *turnConductor, repair *promiseRepairState) error {
	response, interrupted, err := p.streamResponse(brainCtx, cancel, stream, true, metrics, turn, repair)
	if interrupted {
		p.keepCapture.Store(true)
	}
	if err != nil {
		err = mapIdleDeadline(brainCtx, err)
		if interrupted || errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("background result timed out: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, response, err)
		return nil
	}
	p.emitResponse(metrics, events.ResponseReady{Response: response, Interrupted: interrupted, Degraded: metrics.degraded})
	if interrupted {
		turn.finish(TurnInterrupted)
	} else {
		if b, ok := p.Brain.(interface{ AckBackgroundDelivery() }); ok && p.WorkCoordinator() == nil {
			b.AckBackgroundDelivery()
		}
		turn.finish(TurnCompleted)
	}
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return nil
}

// RetireBrain releases a replaced provider without blocking the UI dispatcher.
// Final pipeline teardown joins every retired lifetime, including admission
// reconciliation that must finish before it can cancel a newly created child,
// and a stopped request still ending on the provider.
func (p *Pipeline) RetireBrain(provider brain.Provider) {
	p.retiredBrains.Add(1)
	go func() {
		defer p.retiredBrains.Done()
		settle, cancel := context.WithTimeout(context.Background(), providerSettleTimeout)
		_ = p.awaitRequests(settle, provider)
		cancel()
		if err := brain.CloseProvider(provider); err != nil {
			fmt.Fprintf(os.Stderr, "obey-voice: retired brain cleanup: %v\n", err)
		}
	}()
}
func (p *Pipeline) CloseBrains() error {
	err := brain.CloseProvider(p.Brain)
	p.retiredBrains.Wait()
	return err
}

// HasCapturedInput is read by the serialized host only between turns. A
// barge-in's retained audio takes priority over every unsolicited notification.
func (p *Pipeline) HasCapturedInput() bool {
	if p.keepCapture.Load() {
		return true
	}
	p.captureMu.Lock()
	session := p.captureSession
	p.captureMu.Unlock()
	if owner, ok := session.(interface{ HasCapturedInput() bool }); ok {
		return owner.HasCapturedInput()
	}
	return false
}
