package stt

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type pauseRequest struct {
	scope  work.WorkScope
	id     string
	ctx    context.Context
	cancel bool
	result chan pauseResponse
}

type pauseResponse struct {
	result PauseResult
	err    error
}

// pauseController is deliberately driven by the STT loop. Pause admission
// therefore observes the same detector/segment state as endpointing and cannot
// race a frame that has already crossed that boundary.
type pauseController struct {
	mu       sync.Mutex
	coord    *work.Coordinator
	pending  *pauseRequest
	paused   bool
	permit   work.CapturePermit
	epoch    uint64
	ownerID  string
	token    PauseToken
	captured bool
	active   bool
	stopped  bool
	resumed  bool
}

func newPauseController(ownerID string) *pauseController {
	return &pauseController{ownerID: ownerID}
}

func (p *pauseController) BindCompletionCoordinator(c *work.Coordinator) {
	p.mu.Lock()
	p.coord = c
	p.mu.Unlock()
}

func (p *pauseController) request(ctx context.Context, scope work.WorkScope, requestID string) (PauseResult, error) {
	if requestID == "" {
		return PauseResult{}, errors.New("stt: empty completion request id")
	}
	r := &pauseRequest{scope: scope, id: requestID, ctx: ctx, result: make(chan pauseResponse, 1)}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return PauseResult{}, errors.New("stt: session closed")
	}
	if p.pending != nil || p.paused {
		p.mu.Unlock()
		return PauseResult{Decision: PauseAcknowledged}, work.ErrConflict
	}
	p.pending = r
	p.mu.Unlock()
	select {
	case response := <-r.result:
		return response.result, response.err
	case <-ctx.Done():
		p.mu.Lock()
		if p.pending == r {
			r.cancel = true
		}
		p.mu.Unlock()
		select {
		case response := <-r.result:
			return response.result, response.err
		default:
		}
		return PauseResult{}, ctx.Err()
	}
}

// boundary is called by a loop after it has fed the current frame to its VAD
// and before endpoint finalization. It returns whether the loop must suppress
// endpoint work while yielded, plus whether speech revoked a prior permit.
func (p *pauseController) boundary(speaking, finalizing, retained bool) (yielded, revoked bool, event YieldedForCompletion) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = speaking || finalizing || retained
	if p.pending != nil {
		r := p.pending
		p.pending = nil
		if r.cancel || r.ctx.Err() != nil {
			r.result <- pauseResponse{err: r.ctx.Err()}
			return false, false, YieldedForCompletion{}
		}
		decision := PauseAcknowledged
		switch {
		case finalizing:
			decision = PauseFinalizing
		case retained:
			decision = PauseCapturedInput
		case speaking:
			decision = PauseUserSpeaking
		}
		if decision != PauseAcknowledged {
			r.result <- pauseResponse{result: PauseResult{Decision: decision}}
		} else {
			if p.coord == nil {
				r.result <- pauseResponse{err: errors.New("stt: completion coordinator is not bound")}
			} else {
				p.epoch++
				permit, err := p.coord.AcquireCapturePermit(r.scope, p.ownerID, p.epoch)
				if err != nil {
					r.result <- pauseResponse{err: err}
				} else {
					p.permit = permit
					p.paused = true
					token := PauseToken{Scope: r.scope, RequestID: r.id, Epoch: p.epoch, Permit: permit}
					p.token = token
					p.captured = false
					r.result <- pauseResponse{result: PauseResult{Token: token, Decision: decision}}
					event = YieldedForCompletion{Scope: r.scope, RequestID: r.id, Epoch: p.epoch, Token: token}
				}
			}
		}
	}
	if p.paused && (speaking || retained || finalizing) {
		if p.coord != nil {
			_ = p.coord.RevokeCapturePermit(p.permit)
		}
		p.paused = false
		p.permit = work.CapturePermit{}
		p.captured = true
		revoked = true
	}
	return p.paused, revoked, event
}

func (p *pauseController) resume(ctx context.Context, token PauseToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != token || (!p.paused && !p.captured) {
		return fmt.Errorf("stt: stale completion pause token")
	}
	if p.captured {
		p.captured = false
		p.resumed = true
		return nil
	}
	// The pipeline may release the permit when its completion lease ends
	// before asking this retained session to resume. That is already a valid
	// release; do not turn it into a stale-token failure.
	if !token.Permit.Valid() {
		p.paused = false
		p.permit = work.CapturePermit{}
		p.resumed = true
		return nil
	}
	if p.coord != nil {
		if err := p.coord.ReleaseCapturePermit(token.Permit); err != nil {
			return err
		}
	}
	p.paused = false
	p.permit = work.CapturePermit{}
	p.resumed = true
	return nil
}

func (p *pauseController) hasCapturedInput() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.captured || p.active
}

func (p *pauseController) consumeResumed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.resumed {
		return false
	}
	p.resumed = false
	return true
}

func (p *pauseController) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	if p.paused && p.coord != nil {
		_ = p.coord.RevokeCapturePermit(p.permit)
	}
	p.paused = false
	p.permit = work.CapturePermit{}
	if p.pending != nil {
		p.pending.result <- pauseResponse{err: errors.New("stt: session closed")}
		p.pending = nil
	}
}
