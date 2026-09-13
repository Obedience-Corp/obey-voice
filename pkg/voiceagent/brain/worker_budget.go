package brain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var ErrWorkerBudgetExceeded = errors.New("worker budget exceeded")

type BudgetLimits struct {
	MaxRequests, MaxToolCalls, MaxOutputTokens, MaxRetries int
	MaxRuntime                                             time.Duration
	StrictTools                                            bool
}

type WorkerBudget struct {
	mu                               sync.Mutex
	limits                           BudgetLimits
	started                          time.Time
	requests, tools, output, retries int
	outputObserved                   bool
	outputUnknown                    bool
	requestInFlight                  bool
	// BeforeRequest is called immediately before reserving a provider request.
	// Hosts use it to revalidate global policy and durable allowance.
	BeforeRequest func() error
}

func NewWorkerBudget(limits BudgetLimits) *WorkerBudget {
	return &WorkerBudget{limits: limits, started: time.Now()}
}
func (b *WorkerBudget) Context(ctx context.Context) context.Context {
	// The host owns cancellation and may combine this value with its runtime
	// timer. Avoid creating an uncancelled timer for every provider turn.
	return withWorkerBudget(ctx, b)
}
func (b *WorkerBudget) AcquireRequest(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.BeforeRequest != nil {
		if err := b.BeforeRequest(); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkLocked(); err != nil {
		return err
	}
	if b.limits.MaxRequests > 0 && b.requests >= b.limits.MaxRequests {
		return ErrWorkerBudgetExceeded
	}
	if b.limits.MaxOutputTokens > 0 && (b.outputUnknown || b.requestInFlight || b.output >= b.limits.MaxOutputTokens) {
		return ErrWorkerBudgetExceeded
	}
	b.requestInFlight = true
	b.requests++
	return nil
}
func (b *WorkerBudget) AcquireRetry(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkLocked(); err != nil {
		return err
	}
	if b.retries >= b.limits.MaxRetries {
		return ErrWorkerBudgetExceeded
	}
	b.retries++
	return nil
}
func (b *WorkerBudget) AcquireTool(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkLocked(); err != nil {
		return err
	}
	if b.limits.MaxToolCalls > 0 && b.tools >= b.limits.MaxToolCalls {
		return ErrWorkerBudgetExceeded
	}
	b.tools++
	return nil
}

// RecordUsage finishes one observed request. Zero is conservatively unknown
// because the SDK cannot distinguish an absent count from explicit zero.
func (b *WorkerBudget) RecordUsage(_, generated int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requestInFlight = false
	if generated <= 0 {
		b.outputUnknown = true
		return
	}
	b.outputObserved = true
	if generated > math.MaxInt-b.output {
		b.outputUnknown = true
		b.output = math.MaxInt
		return
	}
	b.output += generated
}

// rejectedRequest releases only a request rejected before generation by the
// model's unsupported-option response. Other errors retain unknown output.
func (b *WorkerBudget) rejectedRequest() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.requestInFlight = false
	b.mu.Unlock()
}

func (b *WorkerBudget) RemainingOutput() int {
	if b == nil || b.limits.MaxOutputTokens <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.outputUnknown {
		return 0
	}
	if !b.outputObserved {
		return b.limits.MaxOutputTokens
	}
	left := b.limits.MaxOutputTokens - b.output
	if left < 0 {
		return 0
	}
	return left
}

type BudgetSnapshot struct {
	Requests, ToolCalls, OutputTokens, Retries int
	OutputObserved                             bool
	Limits                                     BudgetLimits
}

func (b *WorkerBudget) Snapshot() BudgetSnapshot {
	if b == nil {
		return BudgetSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return BudgetSnapshot{b.requests, b.tools, b.output, b.retries, b.outputObserved, b.limits}
}
func (b *WorkerBudget) checkLocked() error {
	if b.limits.MaxOutputTokens > 0 && b.output > b.limits.MaxOutputTokens {
		return fmt.Errorf("%w: provider exceeded output token ceiling", ErrWorkerBudgetExceeded)
	}
	if b.limits.MaxRuntime > 0 && time.Since(b.started) >= b.limits.MaxRuntime {
		return ErrWorkerBudgetExceeded
	}
	return nil
}

type workerBudgetContextKey struct{}

func withWorkerBudget(ctx context.Context, b *WorkerBudget) context.Context {
	return context.WithValue(ctx, workerBudgetContextKey{}, b)
}
func workerBudgetFromContext(ctx context.Context) *WorkerBudget {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(workerBudgetContextKey{}).(*WorkerBudget)
	return b
}

// Strict policy cannot account for arbitrary subprocesses or remote tools.
func (b *WorkerBudget) AllowTool(name string) error {
	if b == nil || !b.limits.StrictTools {
		return nil
	}
	switch name {
	case "list_files", "read_file", "write_file", "read_skill":
		return nil
	}
	return fmt.Errorf("%w: %s is unavailable under strict worker policy", ErrWorkerBudgetExceeded, name)
}

func (b *WorkerBudget) Err() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkLocked()
}
