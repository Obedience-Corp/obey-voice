package decision

import (
	"context"
	"fmt"
	"time"
)

// SmokeResult measures one synthetic choice, not routing accuracy or a benchmark.
type SmokeResult struct {
	Elapsed time.Duration
	Budget  time.Duration
	Runtime string
}

func (m *Manager) Test(ctx context.Context) (SmokeResult, error) {
	m.mu.Lock()
	if m.closed || m.status.Phase != "ready" || m.current == nil {
		m.mu.Unlock()
		return SmokeResult{}, fmt.Errorf("decision runtime is not ready")
	}
	// Only an owned local runtime is eligible; this button never sends data to a
	// hosted provider or an externally configured endpoint.
	if _, ok := m.current.closer.(interface{ RuntimeInfo() string }); !ok {
		m.mu.Unlock()
		return SmokeResult{}, fmt.Errorf("test requires a managed local runtime")
	}
	run, err := m.controller.prepareSmoke(ctx)
	detail := m.status.Detail
	if err != nil {
		m.mu.Unlock()
		return SmokeResult{}, err
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	result, err := run()
	result.Runtime = detail
	return result, err
}

func (c *Controller) prepareSmoke(ctx context.Context) (func() (SmokeResult, error), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.current
	if g == nil || c.busy {
		return nil, fmt.Errorf("decision runtime is unavailable or busy; try again")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.busy = true
	return func() (SmokeResult, error) {
		defer func() { c.mu.Lock(); c.busy = false; c.mu.Unlock() }()
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		stop := context.AfterFunc(g.ctx, cancel)
		defer stop()
		start := time.Now()
		response, err := g.options.Policy.Evaluator.Evaluate(requestCtx, Request{
			State:     map[string]any{"value": 1},
			Questions: map[string]Question{"positive": {Type: "choice", Instructions: "Is value positive?", Criteria: map[string]string{"yes": "The value is greater than zero.", "no": "The value is zero or negative."}}},
		})
		result := SmokeResult{Elapsed: time.Since(start), Budget: g.options.Budget}
		if g.ctx.Err() != nil {
			return result, fmt.Errorf("decision settings changed during test")
		}
		if requestCtx.Err() != nil {
			return result, requestCtx.Err()
		}
		if err != nil {
			return result, err
		}
		if response.Answers["positive"].Choice != "yes" {
			return result, fmt.Errorf("synthetic choice check failed")
		}
		return result, nil
	}, nil
}
