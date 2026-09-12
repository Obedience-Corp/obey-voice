package main

import (
	"context"
	"log"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// observing wraps a production brain.Provider and records the StreamOptions
// each call was given. The worker is only reached through ThinkFull
// (Agency.workerRun), so both entry points are recorded; History is read
// live from the inner provider.
type observing struct {
	inner brain.Provider

	mu          sync.Mutex
	streamCalls int
	fullCalls   int
	lastStream  brain.StreamOptions
	lastFull    brain.StreamOptions
}

// observingOllama embeds the production provider so its optional interfaces
// remain visible to personaruntime.Selector. A wrapper that only implements Provider
// would silently make a production provider look incapable of history
// projection and prompt freezing.
type observingOllama struct {
	*observing
	*brain.OllamaBrain
}

var _ brain.HistoryProjectionProvider = (*observingOllama)(nil)
var _ brain.PersonaPromptFreezer = (*observingOllama)(nil)
var _ brain.CanonicalHistoryProvider = (*observingOllama)(nil)
var _ brain.ContextHistoryProvider = (*observingOllama)(nil)
var _ brain.IdentityHistoryAppender = (*observingOllama)(nil)
var _ brain.CompletionProvider = (*observingOllama)(nil)
var _ brain.DelegationBinding = (*observingOllama)(nil)

func (o *observingOllama) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	o.observeStream(opts)
	return o.OllamaBrain.ThinkStream(ctx, input, opts)
}
func (o *observingOllama) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	o.observeFull(opts)
	return o.OllamaBrain.ThinkFull(ctx, input, opts)
}

// Fresh preserves the production job isolation while sharing only observer
// counters. Without this override the embedded method returns a raw provider,
// so every real worker call bypasses the harness's call/options evidence.
func (o *observingOllama) Fresh() brain.Provider {
	fresh := o.OllamaBrain.Fresh().(*brain.OllamaBrain)
	return &observingOllama{observing: o.observing, OllamaBrain: fresh}
}

func (o *observingOllama) History() []brain.Turn      { return o.OllamaBrain.History() }
func (o *observingOllama) ClearHistory()              { o.OllamaBrain.ClearHistory() }
func (o *observingOllama) LoadHistory(t []brain.Turn) { o.OllamaBrain.LoadHistory(t) }
func (o *observingOllama) Close() error               { return o.OllamaBrain.Close() }

func observeProvider(inner brain.Provider) (brain.Provider, *observing) {
	core := &observing{inner: inner}
	if b, ok := inner.(*brain.OllamaBrain); ok {
		return &observingOllama{observing: core, OllamaBrain: b}, core
	}
	return core, core
}

func (o *observing) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	o.observeStream(opts)
	return o.inner.ThinkStream(ctx, input, opts)
}

func (o *observing) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	o.observeFull(opts)
	return o.inner.ThinkFull(ctx, input, opts)
}

func (o *observing) observeStream(opts brain.StreamOptions) {
	o.mu.Lock()
	o.streamCalls++
	o.lastStream = opts
	o.mu.Unlock()
}
func (o *observing) observeFull(opts brain.StreamOptions) {
	o.mu.Lock()
	o.fullCalls++
	o.lastFull = opts
	o.mu.Unlock()
}

func (o *observing) ClearHistory()                  { o.inner.ClearHistory() }
func (o *observing) History() []brain.Turn          { return o.inner.History() }
func (o *observing) LoadHistory(turns []brain.Turn) { o.inner.LoadHistory(turns) }
func (o *observing) Close() error                   { return brain.CloseProvider(o.inner) }

func (o *observing) calls() (stream, full int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streamCalls, o.fullCalls
}

func (o *observing) streamOptions() brain.StreamOptions {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastStream
}

func (o *observing) fullOptions() brain.StreamOptions {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastFull
}

// stubWorker is the harness self-test worker: answers at once, runs nothing.
// It is injected through Options.Worker and never written into config, so
// workerProviderAllowed never sees it. `just convoflow gate` refuses it.
type stubWorker struct{}

func newStubWorker() *stubWorker {
	log.Printf("convoflow: stub worker (harness self-test, not the local product path)")
	return &stubWorker{}
}

func (s *stubWorker) ThinkStream(_ context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	chunks := make(chan string, 1)
	chunks <- "done"
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (s *stubWorker) ThinkFull(_ context.Context, _ string, _ brain.StreamOptions) (string, error) {
	return "done", nil
}

func (s *stubWorker) ClearHistory()            {}
func (s *stubWorker) History() []brain.Turn    { return nil }
func (s *stubWorker) LoadHistory([]brain.Turn) {}
