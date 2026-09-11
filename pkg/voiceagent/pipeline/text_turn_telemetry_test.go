package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type textTurnTelemetryTTS struct {
	name   string
	tier   string
	warm   string
	write  bool
	err    error
	stream atomic.Pointer[audio.PCMStream]
}

func (p *textTurnTelemetryTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	if p.err != nil {
		return nil, p.err
	}
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	stream.SetSynthesisWarmState(p.warm)
	if p.write {
		if err := stream.Write(make([]float32, 128)); err != nil {
			return nil, err
		}
	}
	stream.Close()
	p.stream.Store(stream)
	return stream, nil
}

func (p *textTurnTelemetryTTS) Available() bool { return true }

func (p *textTurnTelemetryTTS) ListVoices(string, string) []tts.Voice { return nil }

func (p *textTurnTelemetryTTS) ProviderName() string { return p.name }

func (p *textTurnTelemetryTTS) ModelTier() string { return p.tier }

func (p *textTurnTelemetryTTS) SpeechWarmState() string { return p.warm }

type textTurnFallbackPlayer struct {
	calls        atomic.Int32
	failFallback bool
	final        atomic.Pointer[audio.PCMStream]
}

func (p *textTurnFallbackPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if p.calls.Add(1) == 1 {
		go func() {
			for range stream.Frames() {
			}
		}()
		return nil, &tts.ProviderError{
			Provider: "primary",
			Kind:     tts.ProviderErrorWorker,
			Err:      errors.New("primary playback failed"),
		}
	}
	p.final.Store(stream)
	if p.failFallback {
		go func() {
			for range stream.Frames() {
			}
		}()
		return nil, &tts.ProviderError{
			Provider: "fallback",
			Kind:     tts.ProviderErrorWorker,
			Err:      errors.New("fallback playback failed"),
		}
	}
	if _, err := stream.WaitReady(ctx); err != nil {
		return nil, err
	}
	started := make(chan struct{})
	close(started)
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		for range stream.Frames() {
		}
		done <- audio.PlaybackResult{AudioSeconds: 1}
	}()
	return audio.NewPlayback(started, done), nil
}

func (p *textTurnFallbackPlayer) Stop()           {}
func (p *textTurnFallbackPlayer) IsPlaying() bool { return false }
func (p *textTurnFallbackPlayer) Close() error    { return nil }

func TestRunTurnTextModeFallbackKeepsActualPCMAndWarmEvidence(t *testing.T) {
	bus := events.NewBus()
	var got events.TurnMetrics
	events.Subscribe(bus, func(m events.TurnMetrics) { got = m })

	primary := &textTurnTelemetryTTS{name: "primary", tier: "p", warm: events.WarmStateCold}
	fallback := &textTurnTelemetryTTS{name: "fallback", tier: "f", warm: events.WarmStateWarm, write: true}
	p := &Pipeline{
		Brain:       &fakeBrain{chunks: []string{"Hello from text mode."}},
		TTS:         primary,
		TTSFallback: fallback,
		Player:      &textTurnFallbackPlayer{},
		Events:      bus,
	}

	if err := p.RunTurnTextMode(context.Background(), "say hello"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %+v, want one fallback segment", got.Segments)
	}
	segment := got.Segments[0]
	if segment.Provider != "fallback" || !segment.Fallback || segment.WarmState != events.WarmStateWarm {
		t.Fatalf("segment identity = %+v, want actual warm fallback", segment)
	}
	if segment.SynthFirstPCMElapsed <= 0 || segment.SynthEndedElapsed <= 0 || segment.PlaybackStartedElapsed <= 0 {
		t.Fatalf("segment boundaries = %+v, want actual fallback PCM/close/playback timestamps", segment)
	}
	if !got.TTSWarm {
		t.Fatalf("TTSWarm = %v, want true from fallback request evidence", got.TTSWarm)
	}
	if fallback.stream.Load() == nil || segment.SynthEndedElapsed == 0 {
		t.Fatal("fallback stream was not retained or closed")
	}
}

func TestRunTurnTextModeRetainsFailedSynthesisTelemetry(t *testing.T) {
	bus := events.NewBus()
	var got events.TurnMetrics
	events.Subscribe(bus, func(m events.TurnMetrics) { got = m })
	providerErr := &tts.ProviderError{
		Provider: "primary",
		Kind:     tts.ProviderErrorUnavailable,
		Err:      errors.New("worker unavailable"),
	}
	p := &Pipeline{
		Brain:  &fakeBrain{chunks: []string{"This still reaches the transcript."}},
		TTS:    &textTurnTelemetryTTS{name: "primary", tier: "p", warm: events.WarmStateUnknown, err: providerErr},
		Player: &textTurnFallbackPlayer{},
		Events: bus,
	}

	if err := p.RunTurnTextMode(context.Background(), "say something"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %+v, want one failed attempt", got.Segments)
	}
	segment := got.Segments[0]
	if segment.Provider != "primary" || !strings.Contains(segment.Error, "worker unavailable") {
		t.Fatalf("failed segment = %+v, want provider and error", segment)
	}
	if segment.SynthQueuedElapsed <= 0 || segment.SynthStartedElapsed <= 0 {
		t.Fatalf("failed boundaries = %+v, want observed queue/start", segment)
	}
	if segment.PlaybackStartedElapsed != 0 || segment.PlaybackEndedElapsed != 0 {
		t.Fatalf("failed segment claims playback = %+v", segment)
	}
	if !got.Degraded {
		t.Fatalf("Degraded = false, want completed text turn to report degraded speech")
	}
}

func TestRunTurnTextModeRetainsExhaustedPlaybackTelemetry(t *testing.T) {
	bus := events.NewBus()
	var got events.TurnMetrics
	events.Subscribe(bus, func(m events.TurnMetrics) { got = m })
	p := &Pipeline{
		Brain:  &fakeBrain{chunks: []string{"Playback still failed."}},
		TTS:    &textTurnTelemetryTTS{name: "primary", tier: "p", warm: events.WarmStateWarm, write: true},
		Player: &textTurnFallbackPlayer{},
		Events: bus,
	}

	if err := p.RunTurnTextMode(context.Background(), "say it"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %+v, want one failed playback attempt", got.Segments)
	}
	segment := got.Segments[0]
	if segment.Provider != "primary" || !strings.Contains(segment.Error, "primary playback failed") {
		t.Fatalf("failed playback segment = %+v, want provider and playback error", segment)
	}
	if segment.SynthFirstPCMElapsed <= 0 || segment.SynthEndedElapsed <= 0 {
		t.Fatalf("failed playback boundaries = %+v, want observed synthesis boundaries", segment)
	}
	if segment.PlaybackStartedElapsed != 0 || segment.PlaybackEndedElapsed != 0 || !got.Degraded {
		t.Fatalf("failed playback claims audible completion or healthy turn: segment=%+v metrics=%+v", segment, got)
	}
}

func TestRunTurnTextModeRetainsFailedFallbackSynthesis(t *testing.T) {
	bus := events.NewBus()
	var got events.TurnMetrics
	var ready events.ResponseReady
	var turns atomic.Int32
	events.Subscribe(bus, func(m events.TurnMetrics) { got = m })
	events.Subscribe(bus, func(e events.ResponseReady) { ready = e })
	primary := &textTurnTelemetryTTS{name: "primary", tier: "p", warm: events.WarmStateWarm, write: true}
	fallback := &textTurnTelemetryTTS{
		name: "fallback", tier: "f", warm: events.WarmStateCold,
		err: &tts.ProviderError{Provider: "fallback", Kind: tts.ProviderErrorUnavailable, Err: errors.New("fallback unavailable")},
	}
	p := &Pipeline{
		Brain:       &fakeBrain{chunks: []string{"Fallback synthesis failed, but keep this response."}},
		TTS:         primary,
		TTSFallback: fallback,
		Player:      &textTurnFallbackPlayer{},
		Events:      bus,
		OnTurn:      func() { turns.Add(1) },
	}

	if err := p.RunTurnTextMode(context.Background(), "say it"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	assertFailedFallbackTextTurn(t, got, ready, turns.Load(), "fallback unavailable", "fallback")
}

func TestRunTurnTextModeRetainsFailedFallbackPlayback(t *testing.T) {
	bus := events.NewBus()
	var got events.TurnMetrics
	var ready events.ResponseReady
	var turns atomic.Int32
	events.Subscribe(bus, func(m events.TurnMetrics) { got = m })
	events.Subscribe(bus, func(e events.ResponseReady) { ready = e })
	primary := &textTurnTelemetryTTS{name: "primary", tier: "p", warm: events.WarmStateWarm, write: true}
	fallback := &textTurnTelemetryTTS{name: "fallback", tier: "f", warm: events.WarmStateWarm, write: true}
	p := &Pipeline{
		Brain:       &fakeBrain{chunks: []string{"Fallback playback failed, but keep this response."}},
		TTS:         primary,
		TTSFallback: fallback,
		Player:      &textTurnFallbackPlayer{failFallback: true},
		Events:      bus,
		OnTurn:      func() { turns.Add(1) },
	}

	if err := p.RunTurnTextMode(context.Background(), "say it"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	assertFailedFallbackTextTurn(t, got, ready, turns.Load(), "fallback playback failed", "fallback")
}

func assertFailedFallbackTextTurn(t *testing.T, got events.TurnMetrics, ready events.ResponseReady, turns int32, wantError, wantProvider string) {
	t.Helper()
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %+v, want one failed fallback attempt", got.Segments)
	}
	segment := got.Segments[0]
	if segment.Provider != wantProvider || !segment.Fallback || !strings.Contains(segment.Error, wantError) {
		t.Fatalf("failed fallback segment = %+v, want provider=%q fallback=true error containing %q", segment, wantProvider, wantError)
	}
	if segment.SynthQueuedElapsed <= 0 || segment.SynthStartedElapsed <= 0 {
		t.Fatalf("failed fallback boundaries = %+v, want observed queue/start", segment)
	}
	if segment.PlaybackStartedElapsed != 0 || segment.PlaybackEndedElapsed != 0 {
		t.Fatalf("failed fallback claims playback = %+v", segment)
	}
	if !got.Degraded || !ready.Degraded || ready.Response == "" || turns != 1 {
		t.Fatalf("completion = metrics=%+v response=%+v OnTurn=%d, want degraded retained response and one callback", got, ready, turns)
	}
}
