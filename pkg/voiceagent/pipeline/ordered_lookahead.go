package pipeline

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// orderGate keeps playback in admission order while synthesis runs out of it.
//
// PlayStream owns a stream once invoked, so a worker that finished early parks
// here holding its own stream until the handoff.
//
// next advances only across a contiguous run of released indices: a skip — a
// segment with nothing to speak — can finish before a slower index ahead of it.
type orderGate struct {
	mu       sync.Mutex
	next     int
	released map[int]bool
	// advanced is closed and replaced every time next moves, waking parked
	// workers to re-check.
	advanced chan struct{}
}

func newOrderGate() *orderGate {
	return &orderGate{released: make(map[int]bool), advanced: make(chan struct{})}
}

// await blocks until every index before this one has been released. It returns
// ctx's error when the turn is canceled or barged in, and the caller must then
// hand nothing to the player.
func (g *orderGate) await(ctx context.Context, index int) error {
	if g == nil || index <= 0 {
		return nil
	}
	for {
		g.mu.Lock()
		open, advanced := g.next >= index, g.advanced
		g.mu.Unlock()
		if open {
			// The predecessor's release and a cancel land in the same window
			// on a parked worker. Turn cancel does not stop the player, so a
			// canceled turn hands nothing over even with the gate open.
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-advanced:
		}
	}
}

// done releases an index. It is idempotent, and every path out of synthesis
// releases via defer: a missed release wedges the rest of the turn, so the
// defer is the mechanism rather than a call on each path.
func (g *orderGate) done(index int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if index < g.next || g.released[index] {
		return
	}
	g.released[index] = true
	moved := false
	for g.released[g.next] {
		delete(g.released, g.next)
		g.next++
		moved = true
	}
	if moved {
		close(g.advanced)
		g.advanced = make(chan struct{})
	}
}

// fallbackLatch is the turn's one-way switch to the fallback voice, shared by
// every synth worker. Once any segment falls back, the rest of the reply stays
// on that voice: one voice per reply is the point, so a fallback on index k
// applies to k+1 whichever worker picks it up.
//
// It is read when a segment starts synthesizing, so above one worker a sibling
// already in flight on the primary keeps its own result — the latch binds every
// index that starts after the switch, not one that raced it.
type fallbackLatch struct{ stuck atomic.Bool }

func (f *fallbackLatch) latched() bool { return f != nil && f.stuck.Load() }

func (f *fallbackLatch) latch() {
	if f != nil {
		f.stuck.Store(true)
	}
}

// lookahead resolves how many admitted segments a turn may hold ahead of the
// one playing, and how many workers synthesize them.
//
// Workers come from the provider's own concurrency, which counts engine
// instances rather than goroutines (tts.ConcurrentProvider). Depth can never
// sit below it: a worker with no queue slot to draw from is a goroutine that
// never runs.
func (p *Pipeline) lookahead(primary tts.Provider) (depth, workers int) {
	depth = p.TTSLookahead
	if depth < 1 {
		depth = voiceQueueDepth
	}
	workers = tts.ConcurrencyOf(primary)
	if depth < workers {
		p.lookaheadClamped.Do(func() {
			fmt.Fprintf(os.Stderr, "%s obey-voice: tts_lookahead=%d is below the speech engine's concurrency %d; using %d\n",
				time.Now().UTC().Format(time.RFC3339), depth, workers, workers)
		})
		depth = workers
	}
	return depth, workers
}
