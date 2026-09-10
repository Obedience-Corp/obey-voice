package pipeline

import (
	"context"
	"errors"
	"time"
)

// brainTimeout returns the per-turn model deadline and whether it is armed.
func (p *Pipeline) brainTimeout() (time.Duration, bool) {
	if p.BrainTurnTimeout < 0 {
		return 0, false
	}
	if p.BrainTurnTimeout > 0 {
		return p.BrainTurnTimeout, true
	}
	return defaultBrainTurnTimeout, true
}

// withBrainTimeout derives a child context that expires after idle brain work.
// Tokens and tool activity reset the timer; parent cancellation still wins
// immediately.
func (p *Pipeline) withBrainTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d, ok := p.brainTimeout()
	if !ok {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
	alive := true
	bump := func() {
		p.brainDeadlineMu.Lock()
		defer p.brainDeadlineMu.Unlock()
		if !alive || timer == nil {
			return
		}
		if timer.Stop() {
			timer.Reset(d)
		}
	}
	p.brainDeadlineMu.Lock()
	p.brainDeadlineBump = bump
	p.brainDeadlineMu.Unlock()
	return ctx, func() {
		p.brainDeadlineMu.Lock()
		alive = false
		p.brainDeadlineBump = nil
		t := timer
		timer = nil
		p.brainDeadlineMu.Unlock()
		if t != nil {
			t.Stop()
		}
		cancel(nil)
	}
}

func (p *Pipeline) bumpBrainDeadline() {
	p.brainDeadlineMu.Lock()
	bump := p.brainDeadlineBump
	p.brainDeadlineMu.Unlock()
	if bump != nil {
		bump()
	}
}

// mapIdleDeadline turns an idle-timer cancel into DeadlineExceeded so callers
// that already distinguish a stall from a barge-in keep working.
func mapIdleDeadline(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}
