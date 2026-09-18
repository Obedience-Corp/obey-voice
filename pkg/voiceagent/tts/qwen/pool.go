package qwen

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// speechSlot is one Qwen engine instance: one bin/qwen3-tts-worker subprocess
// (or one speech-serve connection), the mutex that serializes its requests,
// and its own warm evidence.
//
// The worker protocol carries one request at a time and a session holds its
// own mutex across the whole request, so a second goroutine against one engine
// only queues on that mutex. More than one slot — more than one subprocess —
// is the only thing that makes two segments synthesize at once.
type speechSlot struct {
	mu      sync.Mutex
	session qwenSpeechSession
	// ref publishes the live session to readers that must not block behind a
	// synthesis: SoftCancel, warmth, and the streaming metrics.
	ref atomic.Pointer[qwenSpeechSession]
	// completed is this slot's own warm evidence: a session is not warm until
	// it has completed a request on that same session, and a worker restart
	// clears it for that slot alone.
	completed atomic.Bool
	// waiting counts callers queued on this slot, so a fully busy pool sends
	// the next request to the shortest line.
	waiting atomic.Int32
}

// live reads this slot's session without taking its mutex.
func (s *speechSlot) live() qwenSpeechSession {
	if s == nil {
		return nil
	}
	if ref := s.ref.Load(); ref != nil && *ref != nil {
		return *ref
	}
	return nil
}

// adopt publishes a freshly opened session on this slot. The caller holds mu.
func (s *speechSlot) adopt(session qwenSpeechSession) {
	s.session = session
	s.ref.Store(&session)
}

// newSpeechPool builds n engine slots. Every provider has at least one, which
// is where the single-session transports (managed Python, the one-shot CLI)
// keep their warm evidence.
func newSpeechPool(n int) []*speechSlot {
	if n < 1 {
		n = 1
	}
	pool := make([]*speechSlot, n)
	for i := range pool {
		pool[i] = &speechSlot{}
	}
	return pool
}

// primarySlot is the first engine instance. A pool of one is the whole pool,
// and the non-native transports never have another.
func (q *Qwen3TTS) primarySlot() *speechSlot { return q.pool[0] }

// acquireSlot returns an engine instance with its mutex held.
//
// It prefers an idle engine and otherwise queues on the shortest line. Round
// robin decides both the scan order and ties, so equal-length queues spread
// rather than piling onto slot 0. Blocking is correct: the caller is already a
// synthesis goroutine whose segment has nowhere else to be.
func (q *Qwen3TTS) acquireSlot() *speechSlot {
	pool := q.pool
	if len(pool) == 1 {
		// One engine: exactly the single mutex this provider always took.
		pool[0].mu.Lock()
		return pool[0]
	}
	start := int(q.dispatch.Add(1)) % len(pool)
	for i := range pool {
		slot := pool[(start+i)%len(pool)]
		if slot.mu.TryLock() {
			return slot
		}
	}
	best := pool[start]
	for i := 1; i < len(pool); i++ {
		if slot := pool[(start+i)%len(pool)]; slot.waiting.Load() < best.waiting.Load() {
			best = slot
		}
	}
	best.waiting.Add(1)
	best.mu.Lock()
	best.waiting.Add(-1)
	return best
}

// startPool opens one session per slot, concurrently: model load is per worker
// and paying it serially would multiply startup by the pool size.
//
// Only the first slot's failure is fatal. A provider whose first engine will
// not start is unusable, but a pool that comes up one worker short still
// speaks — the empty slot opens on its first request, the same path a
// restarted worker takes.
func (q *Qwen3TTS) startPool(ctx context.Context) error {
	pool := q.pool
	errs := make([]error, len(pool))
	var wg sync.WaitGroup
	for i, slot := range pool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = q.startSlot(ctx, slot)
		}()
	}
	wg.Wait()
	return errs[0]
}

// startSlot opens one session and publishes it on the slot.
func (q *Qwen3TTS) startSlot(ctx context.Context, slot *speechSlot) error {
	if q.startSpeech == nil {
		return errors.New("no speech transport configured")
	}
	session, err := q.startSpeech(ctx)
	if err != nil {
		return err
	}
	q.applyStreamPolicy(session)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	slot.adopt(session)
	return nil
}

// closePool tears every engine down, each under its own mutex, so a slot with
// a request in flight finishes before its worker is closed.
func (q *Qwen3TTS) closePool() {
	for _, slot := range q.pool {
		slot.mu.Lock()
		q.discardSpeechSessionLocked(slot)
		slot.mu.Unlock()
	}
}

// cancelActiveAll fans a soft cancel out to every live engine. It is not a
// per-request cancel: nothing here knows which slot holds which segment, so a
// cancel abandons whatever the pool is synthesizing.
func (q *Qwen3TTS) cancelActiveAll() error {
	var errs []error
	for _, slot := range q.pool {
		if session := slot.live(); session != nil {
			if err := session.CancelActive(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// readyWorkers counts engines with a session up right now.
func (q *Qwen3TTS) readyWorkers() int {
	ready := 0
	for _, slot := range q.pool {
		if slot.live() != nil {
			ready++
		}
	}
	return ready
}

// poolLive reports that every engine has a session up. A partially cold pool
// is cold: the next segment may land on the slot that still has to start a
// worker, and that segment pays the full cold-start.
func (q *Qwen3TTS) poolLive() bool {
	if q == nil || len(q.pool) == 0 {
		return false
	}
	return q.readyWorkers() == len(q.pool)
}

// poolCompleted reports that every engine has completed a request on its
// current session. One cold slot makes the pool cold, for the same reason.
func (q *Qwen3TTS) poolCompleted() bool {
	if q == nil || len(q.pool) == 0 {
		return false
	}
	for _, slot := range q.pool {
		if !slot.completed.Load() {
			return false
		}
	}
	return true
}
