package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func completedPlaybackForTest(started bool) *audio.Playback {
	start := make(chan struct{})
	if started {
		close(start)
	}
	done := make(chan audio.PlaybackResult, 1)
	done <- audio.PlaybackResult{AudioSeconds: 1}
	close(done)
	return audio.NewPlayback(start, done)
}

func TestCompletedPlaybackKeepsStartAndPCMEvidence(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := &Pipeline{Events: events.NewBus()}
		metrics := newTurnMetrics()
		stream := audio.NewPCMStream(context.Background())
		if err := stream.SetSampleRate(24000); err != nil {
			t.Fatal(err)
		}
		if err := stream.Write([]float32{.1}); err != nil {
			t.Fatal(err)
		}
		stream.Close()
		observation := synthesisObservation{synthStarted: time.Now(), stream: stream}
		p.handlePlaybackLifecycle("short audio", false, observation, completedPlaybackForTest(true), metrics)
		if len(metrics.segments) != 1 || metrics.segments[0].SynthFirstPCMElapsed <= 0 || metrics.segments[0].PlaybackStartedElapsed <= 0 {
			t.Fatalf("completed short playback lost observed boundaries: %+v", metrics.segments)
		}
	}
}

func TestWatchCompletedPlaybackKeepsStartBeforeFinish(t *testing.T) {
	for i := 0; i < 100; i++ {
		out := make(chan playbackEvent, 2)
		var started atomic.Bool
		watchPlayback(make(chan struct{}), "short audio", 0, false, playbackObservation{}, 0, completedPlaybackForTest(true), &started, out)
		if !started.Load() || len(out) != 2 {
			t.Fatalf("precompleted playback lost start: started=%v events=%d", started.Load(), len(out))
		}
		if first, second := <-out, <-out; first.kind != playbackStarted || second.kind != playbackFinished || second.playStarted.IsZero() {
			t.Fatalf("events reordered or start missing: %+v %+v", first, second)
		}
	}
}
