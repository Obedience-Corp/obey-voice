package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type sustainedFakeProvider struct {
	calls       atomic.Int32
	err         error
	cancelAfter int32
}

func (p *sustainedFakeProvider) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (p *sustainedFakeProvider) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	n := p.calls.Add(1)
	if p.cancelAfter > 0 && n >= p.cancelAfter {
		return "", context.Canceled
	}
	if p.err != nil {
		return "", p.err
	}
	return "ok", nil
}
func (*sustainedFakeProvider) ClearHistory()            {}
func (*sustainedFakeProvider) History() []brain.Turn    { return nil }
func (*sustainedFakeProvider) LoadHistory([]brain.Turn) {}

func TestSustainedLocalWorkerRequiresEffectiveOllama(t *testing.T) {
	for _, cfg := range []*config.Config{
		{VoiceWorkerProvider: "claude", BrainProvider: "ollama"},
		{VoiceWorkerProvider: "stub", BrainProvider: "ollama"},
		{VoiceWorkerProvider: "ollama", VoiceWorkers: func() *config.VoiceWorkers {
			w := config.DefaultVoiceWorkers()
			w.Mode = config.WorkerModeTiered
			return w
		}()},
	} {
		if _, err := newSustainedLocalWorker(cfg, &sustainedFakeProvider{}, nil); err == nil {
			t.Fatalf("config %+v unexpectedly accepted", cfg)
		}
	}
}

func TestSustainedLocalWorkerRepeatsAndStopsOnCancellation(t *testing.T) {
	provider := &sustainedFakeProvider{cancelAfter: 3}
	var completed atomic.Int32
	w, err := newSustainedLocalWorker(&config.Config{VoiceWorkerProvider: "ollama"}, provider, func(int) { completed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = w.ThinkFull(ctx, "load", brain.StreamOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ThinkFull error = %v", err)
	}
	if provider.calls.Load() != 3 || completed.Load() != 2 {
		t.Fatalf("provider calls=%d completed=%d", provider.calls.Load(), completed.Load())
	}
}

func TestSustainedLocalWorkerReturnsProviderError(t *testing.T) {
	w, err := newSustainedLocalWorker(&config.Config{VoiceWorkerProvider: "ollama"}, &sustainedFakeProvider{err: errors.New("provider failed")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.ThinkFull(context.Background(), "load", brain.StreamOptions{})
	if err == nil || err.Error() != "provider failed" {
		t.Fatalf("ThinkFull error = %v", err)
	}
}
