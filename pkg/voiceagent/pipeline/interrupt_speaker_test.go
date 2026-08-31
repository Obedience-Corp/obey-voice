package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// newVerdictController wires a controller like newTestController but with a
// speaker verdict source, for the S3 (barge_in_speakers=enrolled_only)
// fail-open matrix (D7, review #13/#14).
func newVerdictController(capture CaptureMonitor, vad VoiceDetector, playback playbackState, minSpeech int, verdict func() SpeakerVerdict, maxAge time.Duration) *interruptController {
	c := newTestController(capture, vad, playback, minSpeech)
	c.speakerVerdict = verdict
	c.verdictMaxAge = maxAge
	return c
}

// TestInterruptControllerSpeakerVerdictFailOpenMatrix drives the interrupt
// controller with synthetic verdict streams covering every case D7 and
// review #13/#14 specify: only a confident, current, non-enrolled verdict
// blocks a trip. Every other case — including barge_in_speakers=anyone,
// modeled here as a nil verdict source — fails open.
func TestInterruptControllerSpeakerVerdictFailOpenMatrix(t *testing.T) {
	const maxAge = 500 * time.Millisecond

	tests := []struct {
		name     string
		verdict  func() SpeakerVerdict
		wantTrip bool
	}{
		{
			name:     "anyone mode: nil verdict source, never consulted",
			verdict:  nil,
			wantTrip: true,
		},
		{
			name: "healthy + owner: trips",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: true, At: time.Now()}
			},
			wantTrip: true,
		},
		{
			name: "healthy + confident stranger: blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: false, At: time.Now()}
			},
			wantTrip: false,
		},
		{
			name: "unhealthy analyzer: fails open",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: false, HasVerdict: true, Enrolled: false, At: time.Now()}
			},
			wantTrip: true,
		},
		{
			name: "no verdict yet (cold start): fails open",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: false}
			},
			wantTrip: true,
		},
		{
			name: "stale verdict: fails open",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: false, At: time.Now().Add(-time.Second)}
			},
			wantTrip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := newFakeCapture()
			vad := &fakeVAD{}
			playing := &alwaysPlaying{}
			playing.playing.Store(true)

			c := newVerdictController(capture, vad, playing, 3, tt.verdict, maxAge)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := c.watch(ctx, armedNow())

			for range 3 {
				capture.Publish([]float32{0.9, 0.9, 0.9})
				time.Sleep(5 * time.Millisecond)
			}

			if tt.wantTrip {
				select {
				case req := <-out:
					if req.Reason != "barge_in" {
						t.Fatalf("Reason = %q, want barge_in", req.Reason)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("controller did not fire, want a trip")
				}
				return
			}
			select {
			case <-out:
				t.Fatal("controller fired, want it blocked by a confident non-enrolled verdict")
			case <-time.After(150 * time.Millisecond):
			}
		})
	}
}

// TestInterruptControllerSpeakerVerdictRecoversAfterBlock proves a blocked
// stranger does not wedge the watcher: once the verdict source reports an
// enrolled speaker, the still-running watcher (never returned, never reset)
// trips on the very next qualifying chunk.
func TestInterruptControllerSpeakerVerdictRecoversAfterBlock(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	var enrolled atomic.Bool
	verdict := func() SpeakerVerdict {
		return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: enrolled.Load(), At: time.Now()}
	}

	c := newVerdictController(capture, vad, playing, 3, verdict, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	// Stranger talks past the speech threshold: blocked, not dropped.
	for range 3 {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired for a non-enrolled speaker")
	case <-time.After(100 * time.Millisecond):
	}

	// The owner is recognized; the watcher is still running and re-checks on
	// the next chunk without needing to be re-armed or resubscribed.
	enrolled.Store(true)
	capture.Publish([]float32{0.9, 0.9, 0.9})

	select {
	case req := <-out:
		if req.Reason != "barge_in" {
			t.Fatalf("Reason = %q, want barge_in", req.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not recover after the verdict turned enrolled")
	}
}
