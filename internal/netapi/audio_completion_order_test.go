package netapi

import (
	"context"
	"runtime"
	"testing"
)

// A single P lets the nonblocking pump finish its already-buffered stream
// before the waiting owner resumes: both lifecycle signals are ready together.
func TestFanoutCompletedPCMStillReturnsPlaybackReceipt(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	fanout := NewAudioFanout(nil)
	for i := 0; i < 100; i++ {
		stream := bufferedPCM(t, context.Background(), 24000, []float32{.25, -.25})
		playback, err := fanout.PlayStream(context.Background(), stream)
		if err != nil {
			t.Fatalf("completed nonempty stream was rejected: %v", err)
		}
		select {
		case <-playback.Started():
		default:
			t.Fatal("nonempty stream lost its start signal")
		}
		result, ok := <-playback.Done()
		if !ok || result.Err != nil || result.Interrupted {
			t.Fatalf("terminal receipt lost: ok=%v result=%+v", ok, result)
		}
	}
}
