package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// sustainedLocalWorker is a measurement-only adapter. Each successful request
// is a real provider request; clearing history keeps repeated samples bounded
// and makes the load independent of the worker's prior conversation.
type sustainedLocalWorker struct {
	inner        brain.Provider
	completed    func(int)
	requestCount int
	evidence     *workloadEvidence
}

func newSustainedLocalWorker(cfg *config.Config, inner brain.Provider, completed func(int)) (brain.Provider, error) {
	if cfg == nil || inner == nil {
		return nil, fmt.Errorf("sustained worker: provider is not configured")
	}
	provider := strings.TrimSpace(cfg.VoiceWorkerProvider)
	if provider == "" {
		provider = cfg.BrainProvider
	}
	if !strings.EqualFold(provider, "ollama") {
		return nil, fmt.Errorf("sustained worker: requires Ollama, got %q", provider)
	}
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		return nil, fmt.Errorf("sustained worker: tiered assignment is unsupported")
	}
	return &sustainedLocalWorker{inner: inner, completed: completed, evidence: &workloadEvidence{}}, nil
}

func (w *sustainedLocalWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return w.inner.ThinkStream(ctx, input, opts)
}

func (w *sustainedLocalWorker) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	finish := w.evidence.loopStarted()
	defer finish()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		request := w.evidence.requestStarted(time.Now())
		text, err := w.inner.ThinkFull(ctx, input+"\nReturn the next bounded workload result.", opts)
		w.evidence.requestEnded(request, time.Now(), err)
		if err != nil {
			return "", err
		}
		w.requestCount++
		if w.completed != nil {
			w.completed(w.requestCount)
		}
		w.inner.ClearHistory()
		select {
		case <-ctx.Done():
			return text, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func (w *sustainedLocalWorker) ClearHistory()                  { w.inner.ClearHistory() }
func (w *sustainedLocalWorker) History() []brain.Turn          { return w.inner.History() }
func (w *sustainedLocalWorker) LoadHistory(turns []brain.Turn) { w.inner.LoadHistory(turns) }

func (w *sustainedLocalWorker) Fresh() brain.Provider {
	if fresh, ok := w.inner.(brain.FreshProvider); ok {
		return &sustainedLocalWorker{inner: fresh.Fresh(), completed: w.completed, evidence: w.evidence}
	}
	return &sustainedLocalWorker{inner: w.inner, completed: w.completed, evidence: w.evidence}
}

func (w *sustainedLocalWorker) ValidateLegacyWorker(ctx context.Context) error {
	if validator, ok := w.inner.(brain.LegacyWorkerValidator); ok {
		return validator.ValidateLegacyWorker(ctx)
	}
	return nil
}

func (w *sustainedLocalWorker) Close() error {
	if c, ok := w.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
