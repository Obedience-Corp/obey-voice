package pipeline

import "context"

type backgroundProvider interface {
	BackgroundReady() <-chan struct{}
	BackgroundPending() bool
}

// BackgroundReady exposes a signal for a host-owned serialized turn driver.
// The host must not run a background turn concurrently with a user turn.
func (p *Pipeline) BackgroundReady() <-chan struct{} {
	if b, ok := p.Brain.(backgroundProvider); ok {
		return b.BackgroundReady()
	}
	return nil
}
func (p *Pipeline) BackgroundPending() bool {
	if b, ok := p.Brain.(backgroundProvider); ok {
		return b.BackgroundPending()
	}
	return false
}

// RunBackgroundTurn uses the ordinary model, speech queue and turn events,
// without emitting a fictional UserInput or granting a new tool-calling turn.
func (p *Pipeline) RunBackgroundTurn(ctx context.Context) error {
	if !p.BackgroundPending() || ctx.Err() != nil {
		return ctx.Err()
	}
	return p.runTextTurn(ctx, "", true)
}
