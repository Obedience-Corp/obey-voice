package decision

import (
	"context"
	"slices"
	"sync/atomic"
	"time"
)

type WorkResult struct {
	Proposal WorkProposal
	Err      error
	Elapsed  time.Duration
}

// Pending overlaps a decision with already-required admission/preparation. Take
// consumes at most one ready proposal; it never waits or invokes a fallback.
type Pending struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan WorkResult
	taken  atomic.Bool
}

func (p WorkPolicy) Start(ctx context.Context, in WorkInput, budget time.Duration) *Pending {
	if p.Evaluator == nil || ctx.Err() != nil || budget <= 0 || len(in.EnabledTiers) == 0 {
		return nil
	}
	decisionCtx, cancel := context.WithTimeout(ctx, budget)
	pending := &Pending{ctx: ctx, cancel: cancel, done: make(chan WorkResult, 1)}
	in.EnabledTiers = slices.Clone(in.EnabledTiers)
	in.Conversation = slices.Clone(in.Conversation)
	go func() {
		defer cancel()
		start := time.Now()
		proposal, err := p.Decide(decisionCtx, in)
		pending.done <- WorkResult{Proposal: proposal, Err: err, Elapsed: time.Since(start)}
	}()
	return pending
}

func (p *Pending) Take() (WorkResult, bool) {
	if p == nil || !p.taken.CompareAndSwap(false, true) {
		return WorkResult{}, false
	}
	defer p.cancel()
	if p.ctx.Err() != nil {
		return WorkResult{}, false
	}
	select {
	case result := <-p.done:
		return result, p.ctx.Err() == nil
	default:
		return WorkResult{}, false
	}
}

func (p *Pending) Close() {
	if p != nil {
		p.taken.Store(true)
		p.cancel()
	}
}
