package decision

import (
	"context"
	"sync"
	"time"
)

// Point names a host-selected opportunity, never a model-selected capability.
type Point string

const (
	VoiceWork Point = "voice_work"
	TextWork  Point = "text_work"
)

type Options struct {
	Policy              WorkPolicy
	Budget              time.Duration
	VoiceWork, TextWork bool
}

type generation struct {
	options Options
	ctx     context.Context
	cancel  context.CancelFunc
}

// Controller owns optional inference. Its zero value is off. Reconfiguration
// cancels outstanding decisions without cancelling a conversation or a worker.
// At most one evaluator call is in flight, including across reconfiguration.
type Controller struct {
	mu      sync.Mutex
	current *generation
	busy    bool
}

func (c *Controller) Configure(options Options) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		c.current.cancel()
	}
	c.current = nil
	if options.Policy.Evaluator == nil || options.Budget <= 0 || (!options.VoiceWork && !options.TextWork) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.current = &generation{options: options, ctx: ctx, cancel: cancel}
}

func (c *Controller) Close() { c.Configure(Options{}) }

func (c *Controller) Enabled(point Point) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.current
	return g != nil && ((point == VoiceWork && g.options.VoiceWork) || (point == TextWork && g.options.TextWork))
}

// InFlight is a diagnostic snapshot; callers must not wait for it on a turn.
func (c *Controller) InFlight() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy
}

type Attempt struct {
	pending *Pending
	ctx     context.Context
	cancel  context.CancelFunc
	stop    func() bool
}

// Start never does provider I/O on the calling goroutine. Off and busy perform
// no request. A provider that ignores cancellation cannot accumulate more calls.
func (c *Controller) Start(ctx context.Context, point Point, input WorkInput) *Attempt {
	if c == nil || ctx.Err() != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.current
	if g == nil || c.busy || (point != VoiceWork && point != TextWork) ||
		(point == VoiceWork && !g.options.VoiceWork) || (point == TextWork && !g.options.TextWork) {
		return nil
	}
	if !eligibleWork(input) {
		return nil
	}
	c.busy = true
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	// Start must be called with an uncancelled context or its nil shortcut would
	// leave busy set. Cancellation after this point is handled by the evaluator.
	pending := g.options.Policy.start(requestCtx, input, g.options.Budget, func() { c.mu.Lock(); c.busy = false; c.mu.Unlock() })
	if pending == nil {
		c.busy = false
		stop()
		cancel()
		return nil
	}
	return &Attempt{pending: pending, ctx: g.ctx, cancel: cancel, stop: stop}
}

func (a *Attempt) Take() (WorkResult, bool) {
	if a == nil {
		return WorkResult{}, false
	}
	defer a.Close()
	result, ready := a.pending.Take()
	return result, ready && a.ctx.Err() == nil
}

func (a *Attempt) Close() {
	if a != nil {
		a.pending.Close()
		a.stop()
		a.cancel()
	}
}
