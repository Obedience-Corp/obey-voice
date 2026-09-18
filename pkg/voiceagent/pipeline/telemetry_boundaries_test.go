package pipeline

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func TestPartitionSpeechGapKeepsObservedStagesNonOverlapping(t *testing.T) {
	base := time.Unix(100, 0)
	end := base.Add(10 * time.Second)
	got := partitionSpeechGap(base, end, playbackEvent{
		textReady:    base.Add(2 * time.Second),
		queuedAt:     base.Add(3 * time.Second),
		synthStarted: base.Add(4 * time.Second),
		synthEnded:   base.Add(7 * time.Second),
		engine:       ttsEngineIdentity{place: "local", workerStreaming: false},
	})
	if len(got) != 5 {
		t.Fatalf("got %d intervals, want five observed/unknown partitions: %+v", len(got), got)
	}
	for i, gap := range got {
		if gap.EndElapsed <= gap.StartElapsed {
			t.Fatalf("interval %d is empty or reversed: %+v", i, gap)
		}
		if i > 0 && gap.StartElapsed < got[i-1].EndElapsed {
			t.Fatalf("intervals overlap: previous=%+v current=%+v", got[i-1], gap)
		}
	}
	if got[0].Cause != speechGapWaitingText || got[1].Cause != speechGapUnknown || got[2].Cause != speechGapSynthQueue || got[3].Cause != speechGapSynthesis || got[4].Cause != speechGapUnknown {
		t.Fatalf("unexpected stage partition: %+v", got)
	}
}

func TestPartitionSpeechGapDoesNotInferRemoteSynthesis(t *testing.T) {
	base := time.Unix(200, 0)
	got := partitionSpeechGap(base, base.Add(5*time.Second), playbackEvent{
		synthStarted: base.Add(time.Second),
		synthEnded:   base.Add(4 * time.Second),
		engine:       ttsEngineIdentity{place: "lan", workerStreaming: false},
	})
	if len(got) != 1 || got[0].Cause != speechGapUnknown {
		t.Fatalf("remote synthesis was attributed without proof: %+v", got)
	}
}

func TestPartitionSpeechGapRejectsContradictoryBoundaries(t *testing.T) {
	base := time.Unix(300, 0)
	tests := []playbackEvent{
		{
			textReady:    base.Add(3 * time.Second),
			queuedAt:     base.Add(2 * time.Second),
			synthStarted: base.Add(4 * time.Second),
			synthEnded:   base.Add(5 * time.Second),
			engine:       ttsEngineIdentity{place: "local"},
		},
		{
			textReady:    base.Add(4 * time.Second),
			queuedAt:     base.Add(2 * time.Second),
			synthStarted: base.Add(3 * time.Second),
			synthEnded:   base.Add(6 * time.Second),
			engine:       ttsEngineIdentity{place: "local"},
		},
		{
			textReady:    base.Add(4 * time.Second),
			queuedAt:     base.Add(5 * time.Second),
			synthStarted: base.Add(3 * time.Second),
			synthEnded:   base.Add(6 * time.Second),
			engine:       ttsEngineIdentity{place: "local"},
		},
	}
	for i, event := range tests {
		got := partitionSpeechGap(base, base.Add(8*time.Second), event)
		if len(got) != 1 || got[0].Cause != speechGapUnknown || got[0].StartElapsed != 0 || got[0].EndElapsed != 8*time.Second {
			t.Errorf("case %d attributed contradictory boundaries: %+v", i, got)
		}
	}
}

type immediateBoundaryTTS struct {
	name   string
	tier   string
	stream atomic.Pointer[audio.PCMStream]
	callAt atomic.Int64
}

func (p *immediateBoundaryTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	p.callAt.Store(time.Now().UnixNano())
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	if err := stream.Write(make([]float32, 64)); err != nil {
		return nil, err
	}
	stream.Close()
	p.stream.Store(stream)
	return stream, nil
}
func (p *immediateBoundaryTTS) Available() bool                       { return true }
func (p *immediateBoundaryTTS) ListVoices(string, string) []tts.Voice { return nil }
func (p *immediateBoundaryTTS) ProviderName() string                  { return p.name }
func (p *immediateBoundaryTTS) ModelTier() string                     { return p.tier }

type fallbackBoundaryPlayer struct {
	calls   atomic.Int32
	primary atomic.Pointer[audio.PCMStream]
	final   atomic.Pointer[audio.PCMStream]
}

func (p *fallbackBoundaryPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if p.calls.Add(1) == 1 {
		p.primary.Store(stream)
		go func() {
			for range stream.Frames() {
			}
		}()
		return nil, &tts.ProviderError{Provider: "primary", Kind: tts.ProviderErrorWorker, Err: fmt.Errorf("primary playback failed")}
	}
	p.final.Store(stream)
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
func (p *fallbackBoundaryPlayer) Stop()           {}
func (p *fallbackBoundaryPlayer) IsPlaying() bool { return false }
func (p *fallbackBoundaryPlayer) Close() error    { return nil }

func TestPlaybackFallbackUsesFinalStreamBoundariesAndIdentity(t *testing.T) {
	primary := &immediateBoundaryTTS{name: "primary", tier: "p"}
	fallback := &immediateBoundaryTTS{name: "fallback", tier: "f"}
	player := &fallbackBoundaryPlayer{}
	p := &Pipeline{TTS: primary, TTSFallback: fallback, Player: player, Events: events.NewBus()}
	out := make(chan playbackEvent, 2)
	if !p.synthesizeSegment(context.Background(), make(chan struct{}), "fallback boundary", 0, new(atomic.Bool), out, nil) {
		t.Fatal("synthesizeSegment returned false after fallback playback was accepted")
	}
	first := <-out
	finished := first
	if first.kind == playbackStarted {
		finished = <-out
	}
	if finished.kind != playbackFinished {
		t.Fatalf("terminal event = %v, want finished", finished.kind)
	}
	finalStream := fallback.stream.Load()
	if finished.synthFirstPCM != finalStream.FirstWrittenAt() || finished.synthEnded != finalStream.ClosedAt() {
		t.Fatalf("boundaries used primary stream: firstPCM=%v/%v synthEnd=%v/%v", finished.synthFirstPCM, finalStream.FirstWrittenAt(), finished.synthEnded, finalStream.ClosedAt())
	}
	if finished.engine.provider != "fallback" || finished.engine.tier != "f" {
		t.Fatalf("finished identity = %+v, want fallback provider", finished.engine)
	}
	if primary.stream.Load() == finalStream {
		t.Fatal("fallback watcher retained primary stream")
	}
	m := newTurnMetrics()
	if first.kind == playbackStarted {
		p.applyPlaybackEvent(first, m, new(atomic.Int64))
	}
	p.applyPlaybackEvent(finished, m, new(atomic.Int64))
	if got := m.snapshot().TTSProvider; got != "fallback" {
		t.Fatalf("turn provider = %q, want actual first segment fallback", got)
	}
}

func TestFailedSynthesisRetainsAttemptTelemetryWithoutPlayback(t *testing.T) {
	p := &Pipeline{TTS: &describedErrorTTS{name: "primary", tier: "p"}, Events: events.NewBus()}
	m := newTurnMetrics()
	queued := time.Now()
	time.Sleep(time.Millisecond)
	outcome := p.synthesizeSegmentObserved(context.Background(), make(chan struct{}), synthWork{
		sentence: "failed sentence", index: 4, textReady: queued, queuedAt: queued,
	}, nil, new(atomic.Bool), make(chan playbackEvent, 1), nil)
	if outcome.enqueued || outcome.failure == nil {
		t.Fatalf("outcome = %+v, want failed not-enqueued telemetry", outcome)
	}
	p.applyPlaybackEvent(*outcome.failure, m, new(atomic.Int64))
	snapshot := m.snapshot()
	if len(snapshot.Segments) != 1 {
		t.Fatalf("segments = %+v, want one failed attempt", snapshot.Segments)
	}
	segment := snapshot.Segments[0]
	if segment.Provider != "primary" || segment.Error == "" || len(segment.Exclusions) == 0 {
		t.Fatalf("failed segment = %+v, want provider, error, and exclusion", segment)
	}
	if segment.SynthStartedElapsed <= 0 || segment.SynthQueuedElapsed < 0 {
		t.Fatalf("failed segment boundaries = %+v, want observed queue/start", segment)
	}
	if segment.PlaybackStartedElapsed != 0 || segment.PlaybackEndedElapsed != 0 {
		t.Fatalf("failed segment claims playback boundaries: %+v", segment)
	}
}

func TestSynthesisEndPrefersProducerCloseOverPlayerDrain(t *testing.T) {
	stream := audio.NewPCMStream(context.Background())
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	producerClosed := stream.ClosedAt()
	playerObserved := producerClosed.Add(time.Second)
	if got := synthesisEnd(stream, audio.PlaybackResult{SynthesisEndedAt: playerObserved}); !got.Equal(producerClosed) {
		t.Fatalf("synthesisEnd = %v, want producer close %v", got, producerClosed)
	}
}
