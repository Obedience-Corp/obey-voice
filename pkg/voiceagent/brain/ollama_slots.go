package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

// errPreempted is the cause a background job's request is cancelled with when
// a live request needs the model's slot.
var errPreempted = errors.New("model slot handed to a live request")

// slotKey names one model on one Ollama server. Ollama serves each loaded
// model with a fixed number of parallel slots (OLLAMA_NUM_PARALLEL, 1 unless
// set), shared by every client of that server.
type slotKey struct {
	host  string
	model string
}

var (
	slotGatesMu sync.Mutex
	slotGates   = map[slotKey]*slotGate{}
)

// slotGate orders the requests this process sends to one model so a live
// request is never stuck behind a background job's request. Persona and
// worker brains are separate OllamaBrain values, so the gate is shared by
// key, not held by a brain.
//
// A live request takes a free slot, or cancels a job request holding one and
// waits for it to return. A job request starts only while no live request is
// waiting and a slot is free; a cancelled one waits the same way and is sent
// again. Nothing here uses a clock: every wait ends on a release or on the
// caller's own context.
type slotGate struct {
	mu          sync.Mutex
	changed     chan struct{}
	capacity    int
	liveWaiting int
	liveRunning int
	jobs        []*jobSlot
}

// jobSlot is one job request holding a slot.
type jobSlot struct {
	cancel    context.CancelCauseFunc
	preempted bool
	// pinned is set once the request delivered output: it can no longer be
	// sent again without repeating that output, so it is never cancelled.
	pinned bool
}

func gateFor(key slotKey) *slotGate {
	slotGatesMu.Lock()
	defer slotGatesMu.Unlock()
	g := slotGates[key]
	if g == nil {
		g = &slotGate{changed: make(chan struct{}), capacity: 1}
		slotGates[key] = g
	}
	return g
}

func (g *slotGate) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// waitLocked releases the lock until the gate changes or ctx ends.
func (g *slotGate) waitLocked(ctx context.Context) error {
	ch := g.changed
	g.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
	g.mu.Lock()
	return ctx.Err()
}

func (g *slotGate) setCapacityLocked(capacity int) {
	if capacity < 1 {
		capacity = 1
	}
	if capacity != g.capacity {
		g.capacity = capacity
		g.notifyLocked()
	}
}

// acquireLive admits a live request, cancelling job requests that hold the
// slots it needs. It never cancels another live request: when only live
// requests fill the slots, Ollama queues this one behind them.
func (g *slotGate) acquireLive(ctx context.Context, capacity int) (release func(), err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setCapacityLocked(capacity)
	g.liveWaiting++
	for g.liveRunning+len(g.jobs) >= g.capacity && g.preemptLocked(g.liveRunning+len(g.jobs)-g.capacity+1) {
		if err := g.waitLocked(ctx); err != nil {
			g.liveWaiting--
			g.notifyLocked()
			return nil, err
		}
	}
	g.liveWaiting--
	g.liveRunning++
	return func() {
		g.mu.Lock()
		g.liveRunning--
		g.notifyLocked()
		g.mu.Unlock()
	}, nil
}

// preemptLocked makes sure need job requests are cancelled, newest first,
// and reports whether any cancelled one has yet to return.
func (g *slotGate) preemptLocked(need int) bool {
	pending := 0
	for _, s := range g.jobs {
		if s.preempted {
			pending++
		}
	}
	for i := len(g.jobs) - 1; i >= 0 && pending < need; i-- {
		if s := g.jobs[i]; !s.preempted && !s.pinned {
			s.preempted = true
			s.cancel(errPreempted)
			pending++
		}
	}
	return pending > 0
}

// acquireJob waits until no live request is waiting and a slot is free.
func (g *slotGate) acquireJob(ctx context.Context, capacity int, cancel context.CancelCauseFunc) (*jobSlot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setCapacityLocked(capacity)
	for g.liveWaiting > 0 || g.liveRunning+len(g.jobs) >= g.capacity {
		if err := g.waitLocked(ctx); err != nil {
			return nil, err
		}
	}
	s := &jobSlot{cancel: cancel}
	g.jobs = append(g.jobs, s)
	return s, nil
}

// claimDelivery pins s before its first output reaches the caller. It
// returns false when s was already cancelled, so that output is dropped and
// the request is sent again.
func (g *slotGate) claimDelivery(s *jobSlot) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.preempted {
		return false
	}
	s.pinned = true
	return true
}

// releaseJob frees s and reports whether it was cancelled for a live request.
func (g *slotGate) releaseJob(s *jobSlot) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, held := range g.jobs {
		if held == s {
			g.jobs = append(g.jobs[:i], g.jobs[i+1:]...)
			break
		}
	}
	g.notifyLocked()
	return s.preempted
}

// slotKey is the gate this brain's requests for model pass through.
func (o *OllamaBrain) slotKey(model string) slotKey {
	host := ""
	if o.cfg != nil {
		host = strings.TrimRight(strings.TrimSpace(o.cfg.OllamaHost), "/")
	}
	if host == "" {
		// Without a configured server, requests share a gate only with the
		// brain's own client.
		host = clientIdentity(o.client)
	}
	return slotKey{host: host, model: model}
}

func clientIdentity(c *api.Client) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("client:%p", c)
}

// numParallel is how many requests Ollama serves at once for one model.
func (o *OllamaBrain) numParallel() int {
	if o.cfg == nil || o.cfg.OllamaNumParallel < 1 {
		return 1
	}
	return o.cfg.OllamaNumParallel
}

// gatedChat sends req through the model's slot gate. A background job's
// request (its context carries a job id) gives way to live requests: one
// cancelled before it delivered anything waits for the live request and is
// sent again, so its caller sees a single answer. Warmups bypass the gate.
func (o *OllamaBrain) gatedChat(ctx context.Context, req *api.ChatRequest, fn api.ChatResponseFunc) error {
	if modelRequestPurpose(ctx) == PurposeWarmup || o.client == nil {
		return observedChat(ctx, &o.requestObserver, o.client, req, fn)
	}
	g := gateFor(o.slotKey(req.Model))
	if job.IDFromContext(ctx) == "" {
		release, err := g.acquireLive(ctx, o.numParallel())
		if err != nil {
			return err
		}
		defer release()
		return observedChat(ctx, &o.requestObserver, o.client, req, fn)
	}
	for {
		attempt, cancel := context.WithCancelCause(ctx)
		slot, err := g.acquireJob(ctx, o.numParallel(), cancel)
		if err != nil {
			cancel(nil)
			return err
		}
		delivered := false
		err = observedChat(attempt, &o.requestObserver, o.client, req, func(resp api.ChatResponse) error {
			if !delivered {
				if !g.claimDelivery(slot) {
					return errPreempted
				}
				delivered = true
			}
			return fn(resp)
		})
		preempted := g.releaseJob(slot)
		cancel(nil)
		if err != nil && preempted && !delivered && ctx.Err() == nil {
			continue
		}
		return err
	}
}
