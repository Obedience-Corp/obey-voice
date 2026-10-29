package audio

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestRecordingFeedDoesNotBlockAndReportsTailLoss(t *testing.T) {
	var feed RecordingFeed
	var enabled atomic.Bool
	enabled.Store(true)
	frames, stop := feed.SubscribeRecording(1, enabled.Load)
	frame := Frame{Samples: []float32{.5}, SampleRate: SampleRate, Channels: 1, StartedAt: time.Now()}
	feed.Publish(frame)
	for range 500 {
		feed.Publish(frame)
	} // full consumer cannot stall a device
	enabled.Store(false)
	feed.Publish(frame) // input pause is intentional, not queue loss
	stats := stop()
	if stats.DroppedFrames != 500 {
		t.Fatalf("loss=%+v", stats)
	}
	if got := <-frames; len(got.Samples) != 1 || got.Samples[0] != .5 {
		t.Fatal(got)
	}
	if _, ok := <-frames; ok {
		t.Fatal("stop did not close the subscription")
	}
	feed.Publish(frame)
	if again := stop(); again != stats {
		t.Fatal("stop is not idempotent")
	}
}

func TestCaptureRecordingUsesProcessedCallbacksWithoutDrainingSTT(t *testing.T) {
	c := NewCapture()
	c.SetFrontend(zeroingFrontend{})
	frames, stop := c.SubscribeRecording(4, nil)
	defer stop()
	pcm := make([]float32, ChunkSize)
	for i := range pcm {
		pcm[i] = .5
	}
	c.ingestCapture(pcm, time.Now(), 1)
	got := <-frames
	if len(got.Samples) != ChunkSize || got.Samples[0] != 0 || got.StartedAt.IsZero() {
		t.Fatalf("frame=%+v", got)
	}
	stt, err := c.ReadFrame(t.Context())
	if err != nil || len(stt.Samples) != ChunkSize {
		t.Fatalf("recording consumed STT audio: %+v %v", stt, err)
	}
}
