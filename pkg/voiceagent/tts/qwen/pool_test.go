package qwen

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// pooledSession is a fake worker that can be held inside a synthesis, so a
// test can watch two engines run at once without a subprocess.
type pooledSession struct {
	entered  chan struct{}
	release  <-chan struct{}
	fail     bool
	calls    atomic.Int32
	canceled atomic.Int32
	closed   atomic.Int32
}

func (s *pooledSession) SynthesizeToStream(ctx context.Context, _ tts.SynthesisRequest, stream *audio.PCMStream) error {
	s.calls.Add(1)
	s.entered <- struct{}{}
	if s.fail {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorWorker, Err: errors.New("worker died")}
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
		return err
	}
	return stream.Write([]float32{0.1, 0.2})
}

func (s *pooledSession) CancelActive() error { s.canceled.Add(1); return nil }
func (s *pooledSession) Presets() []string   { return nil }
func (s *pooledSession) Streaming() bool     { return false }
func (s *pooledSession) Close()              { s.closed.Add(1) }

// poolHarness is a native provider whose workers are fakes. failFirst makes
// the first worker handed out die on its request, which is the restartable
// failure the provider recovers from.
type poolHarness struct {
	q         *Qwen3TTS
	release   chan struct{}
	mu        sync.Mutex
	sessions  []*pooledSession
	failFirst bool
}

func newPoolHarness(t *testing.T, workers int) *poolHarness {
	t.Helper()
	h := &poolHarness{release: make(chan struct{})}
	q := newQwen3TTS("qwen3-tts-worker", t.TempDir(), time.Minute, nil)
	q.native = true
	q.alive.Store(true)
	q.pool = newSpeechPool(workers)
	q.startSpeech = func(context.Context) (qwenSpeechSession, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		session := &pooledSession{
			entered: make(chan struct{}, 8),
			release: h.release,
			fail:    h.failFirst && len(h.sessions) == 0,
		}
		h.sessions = append(h.sessions, session)
		return session, nil
	}
	h.q = q
	t.Cleanup(h.releaseAll)
	return h
}

func (h *poolHarness) releaseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

func (h *poolHarness) started() []*pooledSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*pooledSession(nil), h.sessions...)
}

// synth runs one request to completion and reports its error.
func (h *poolHarness) synth(ctx context.Context) error {
	stream := audio.NewPCMStream(ctx)
	go func() {
		for range stream.Frames() {
		}
	}()
	err := h.q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream)
	stream.Close()
	return err
}

// awaitBusyWorker blocks until one engine has taken a request and returns it.
func (h *poolHarness) awaitBusyWorker(t *testing.T) *pooledSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, session := range h.started() {
			select {
			case <-session.entered:
				return session
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	t.Fatal("no worker took the first request")
	return nil
}

func awaitEntry(t *testing.T, session *pooledSession) {
	t.Helper()
	select {
	case <-session.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("a worker never received its request")
	}
}

// One slot is one subprocess. The pool is fixed at start because each worker
// loads the model, and a segment must never wait out a cold start.
func TestNativePoolStartsOneWorkerPerSlot(t *testing.T) {
	h := newPoolHarness(t, 3)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	if got := len(h.started()); got != 3 {
		t.Fatalf("started %d workers, want 3", got)
	}
	if got := h.q.readyWorkers(); got != 3 {
		t.Fatalf("readyWorkers = %d, want 3", got)
	}
	seen := map[qwenSpeechSession]bool{}
	for _, slot := range h.q.pool {
		session := slot.live()
		if session == nil {
			t.Fatal("a slot came up without a session")
		}
		if seen[session] {
			t.Fatal("two slots share one worker: the pool would serialize on that session's mutex")
		}
		seen[session] = true
	}
	if got := h.q.Concurrency(); got != 3 {
		t.Fatalf("Concurrency = %d, want 3: the lookahead depth is sized from it", got)
	}
}

// The point of the pool: two segments synthesize at the same time, on
// different engines.
func TestRequestsRunOnSeparateWorkersAtOnce(t *testing.T) {
	h := newPoolHarness(t, 2)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- h.synth(ctx) }()
	}
	// Both must be inside a synthesis before either is released. With one
	// engine the second would still be queued on the first one's mutex.
	for _, session := range h.started() {
		awaitEntry(t, session)
	}
	h.releaseAll()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("synthesizeNativeStream: %v", err)
		}
	}
	for i, session := range h.started() {
		if got := session.calls.Load(); got != 1 {
			t.Errorf("worker %d ran %d requests, want 1 each", i, got)
		}
	}
}

// A worker that is busy must not be the one a free request waits behind.
func TestBusyWorkerDoesNotBlockAnIdleOne(t *testing.T) {
	h := newPoolHarness(t, 2)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	held := make(chan error, 1)
	go func() { held <- h.synth(ctx) }()
	busy := h.awaitBusyWorker(t)

	done := make(chan error, 1)
	go func() { done <- h.synth(ctx) }()
	// The idle engine must be the one that takes it. awaitEntry fails the
	// test if the request is instead queued behind the busy worker.
	for _, session := range h.started() {
		if session != busy {
			awaitEntry(t, session)
		}
	}
	h.releaseAll()
	if err := <-done; err != nil {
		t.Fatalf("second request: %v", err)
	}
	if err := <-held; err != nil {
		t.Fatalf("held request: %v", err)
	}
}

// One engine still means one request at a time: the second caller waits on
// that engine's mutex, exactly as the single-session provider always did.
func TestSingleWorkerStillSerializes(t *testing.T) {
	h := newPoolHarness(t, 1)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errs := make(chan error, 2)
	go func() { errs <- h.synth(ctx) }()
	session := h.started()[0]
	awaitEntry(t, session)

	go func() { errs <- h.synth(ctx) }()
	select {
	case <-session.entered:
		t.Fatal("two requests entered one worker at once")
	case <-time.After(200 * time.Millisecond):
	}

	h.releaseAll()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("synthesizeNativeStream: %v", err)
		}
	}
	if got := len(h.started()); got != 1 {
		t.Fatalf("started %d workers for qwen_tts_workers=1, want 1", got)
	}
	if got := session.calls.Load(); got != 2 {
		t.Fatalf("the single worker ran %d requests, want both", got)
	}
}

// Barge-in ends the turn, so the cancel has to reach whatever every engine is
// synthesizing. It is not a per-request cancel and never was.
func TestSoftCancelReachesEveryWorker(t *testing.T) {
	h := newPoolHarness(t, 3)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	if err := h.q.SoftCancel("nqwen-1"); err != nil {
		t.Fatalf("SoftCancel: %v", err)
	}
	for i, session := range h.started() {
		if got := session.canceled.Load(); got != 1 {
			t.Errorf("worker %d saw %d cancels, want 1", i, got)
		}
	}
}

func TestDeleteTearsDownEveryWorker(t *testing.T) {
	h := newPoolHarness(t, 3)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	h.q.Delete()
	for i, session := range h.started() {
		if got := session.closed.Load(); got != 1 {
			t.Errorf("worker %d closed %d times, want 1", i, got)
		}
	}
	if got := h.q.readyWorkers(); got != 0 {
		t.Errorf("readyWorkers = %d after Delete, want 0", got)
	}
	if h.q.Available() {
		t.Error("provider is still available after Delete")
	}
	h.q.Delete()
	for i, session := range h.started() {
		if got := session.closed.Load(); got != 1 {
			t.Errorf("worker %d closed %d times after a second Delete, want 1", i, got)
		}
	}
}

// A pool that cannot start is a provider the caller never receives, so the
// engines that did come up have no other owner: startPool closes them itself.
// Slot 0's failure is the fatal one, so the sibling is brought up first to put
// the pool in exactly that state.
func TestFailedPoolStartClosesTheWorkersThatCameUp(t *testing.T) {
	h := newPoolHarness(t, 2)
	if err := h.q.startSlot(context.Background(), h.q.pool[1]); err != nil {
		t.Fatalf("startSlot: %v", err)
	}
	survivor := h.started()[0]
	h.q.startSpeech = func(context.Context) (qwenSpeechSession, error) {
		return nil, errors.New("worker did not start")
	}

	if err := h.q.startPool(context.Background()); err == nil {
		t.Fatal("startPool reported success with a dead first engine")
	}
	if got := survivor.closed.Load(); got != 1 {
		t.Fatalf("the engine that came up was closed %d times, want 1: a failed start leaves its qwen3-tts-worker running", got)
	}
	for i, slot := range h.q.pool {
		if slot.live() != nil {
			t.Errorf("slot %d still holds a session after a failed start", i)
		}
	}
	if got := h.q.readyWorkers(); got != 0 {
		t.Errorf("readyWorkers = %d after a failed start, want 0", got)
	}
	if h.q.Available() {
		t.Error("provider reports available after a failed start")
	}
	// Shutdown after a failed start must not close the same worker twice.
	h.q.Delete()
	if got := survivor.closed.Load(); got != 1 {
		t.Fatalf("worker closed %d times after Delete, want 1", got)
	}
}

// A dead worker is one slot's problem. Taking the pool down with it would turn
// a recoverable failure into a whole-turn fallback.
func TestDeadWorkerRestartsOnlyItsOwnSlot(t *testing.T) {
	h := newPoolHarness(t, 2)
	h.failFirst = true
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	h.releaseAll()
	started := h.started()
	dead, survivor := started[0], started[1]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Drive requests until the doomed worker has taken one. Dispatch is
	// round-robin, so two are enough for a pool of two.
	for range 2 {
		if err := h.synth(ctx); err != nil {
			t.Fatalf("synthesizeNativeStream: %v", err)
		}
	}

	if got := dead.closed.Load(); got != 1 {
		t.Fatalf("the failed worker was closed %d times, want 1", got)
	}
	if got := survivor.closed.Load(); got != 0 {
		t.Fatalf("the healthy worker was closed %d times, want 0: one dead worker took the pool down", got)
	}
	if got := len(h.started()); got != 3 {
		t.Fatalf("started %d workers, want 3 (two at start, one replacement)", got)
	}
	if got := h.q.readyWorkers(); got != 2 {
		t.Fatalf("readyWorkers = %d, want 2", got)
	}
}

// Warmth is a property of the pool, not of whichever engine answered last: the
// next segment may land on the slot that has not paid its first request yet.
func TestPoolIsColdUntilEveryWorkerHasCompleted(t *testing.T) {
	h := newPoolHarness(t, 2)
	if err := h.q.startPool(context.Background()); err != nil {
		t.Fatalf("startPool: %v", err)
	}
	h.releaseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.synth(ctx); err != nil {
		t.Fatalf("synthesizeNativeStream: %v", err)
	}
	if h.q.SpeechWarm() {
		t.Fatal("a pool with one untried worker reported warm")
	}
	if got := h.q.SpeechWarmState(); got != "cold" {
		t.Fatalf("SpeechWarmState = %q, want cold", got)
	}
	if err := h.synth(ctx); err != nil {
		t.Fatalf("synthesizeNativeStream: %v", err)
	}
	if !h.q.SpeechWarm() {
		t.Fatal("every worker completed a request and the pool still reported cold")
	}
	if got := h.q.SpeechWarmState(); got != "warm" {
		t.Fatalf("SpeechWarmState = %q, want warm", got)
	}
}

// Only the local native pool runs more than one engine.
func TestConcurrencyIsOneOffTheNativeLocalPath(t *testing.T) {
	remote := newQwen3TTS("", "speech:7263", time.Minute, nil)
	remote.native = true
	remote.remote = true
	remote.pool = newSpeechPool(3) // nothing sizes it this way; the guard is the point
	if got := remote.Concurrency(); got != 1 {
		t.Errorf("lan Concurrency = %d, want 1: how many workers the speech host runs is its own setting", got)
	}

	managed := newQwen3TTS("qwen3-tts", t.TempDir(), time.Minute, nil)
	managed.managed = true
	if got := managed.Concurrency(); got != 1 {
		t.Errorf("managed Concurrency = %d, want 1", got)
	}
}
