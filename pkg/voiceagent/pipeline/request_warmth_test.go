package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestPlaybackMetricsUseActualRequestWarmthAfterRestart(t *testing.T) {
	stream := audio.NewPCMStream(context.Background())
	stream.SetSynthesisWarmState(events.WarmStateCold)
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	if err := stream.Write([]float32{0, 0}); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	started := make(chan struct{})
	close(started)
	done := make(chan audio.PlaybackResult, 1)
	out := make(chan playbackEvent, 2)
	loopDone := make(chan struct{})
	defer close(loopDone)
	go watchPlayback(loopDone, "restarted request", 0, false, playbackObservation{
		stream: stream, warmState: events.WarmStateWarm,
		engine:       ttsEngineIdentity{provider: "qwen3-tts", place: "local"},
		synthStarted: time.Now().Add(-time.Second),
	}, time.Second, audio.NewPlayback(started, done), new(atomic.Bool), out)
	select {
	case e := <-out:
		if e.kind != playbackStarted {
			t.Fatalf("first event = %v, want playback start", e.kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback start was not observed")
	}
	done <- audio.PlaybackResult{AudioSeconds: 1}
	select {
	case e := <-out:
		m := newTurnMetrics()
		m.recordSegment(e)
		got := m.snapshot()
		if got.TTSWarm || got.Segments[0].WarmState != events.WarmStateCold {
			t.Fatalf("cold retry retained pre-request warm state: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback completion was not observed")
	}

	failure := synthesisFailureEvent("failed retry", 0, synthesisObservation{
		stream: stream, warmState: events.WarmStateWarm,
	}, context.DeadlineExceeded)
	if failure.warmState != events.WarmStateCold || failure.synthEnded != stream.ClosedAt() {
		t.Fatalf("failed retry lost request evidence: %+v", failure)
	}
}
