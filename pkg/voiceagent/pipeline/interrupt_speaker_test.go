package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// newVerdictController wires a controller like newTestController but with a
// speaker verdict source, for the S3 (barge_in_speakers=enrolled_only)
// enrolled-speaker admission matrix.
func newVerdictController(capture CaptureMonitor, vad VoiceDetector, playback playbackState, minSpeech int, verdict func() SpeakerVerdict, maxAge time.Duration) *interruptController {
	c := newTestController(capture, vad, playback, minSpeech)
	c.speakerVerdict = verdict
	c.verdictMaxAge = maxAge
	return c
}

// Enrolled-only requires a fresh positive match; unavailable recognition must
// not let playback echo interrupt the assistant.
func TestInterruptControllerSpeakerVerdictMatrix(t *testing.T) {
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
			name: "unhealthy analyzer: blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: false, HasVerdict: true, Enrolled: false, At: time.Now()}
			},
			wantTrip: false,
		},
		{
			name: "no verdict yet (cold start): blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: false}
			},
			wantTrip: false,
		},
		{
			name: "stale verdict: blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: false, At: time.Now().Add(-time.Second)}
			},
			wantTrip: false,
		},
		{
			name: "stale enrolled verdict: blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: true, At: time.Now().Add(-time.Second)}
			},
		},
		{
			name: "unstamped enrolled verdict: blocked",
			verdict: func() SpeakerVerdict {
				return SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: true}
			},
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
				t.Fatal("controller fired, want it blocked without a fresh enrolled match")
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

func TestInterruptSpeakerPolicyAlsoGatesPauseRetention(t *testing.T) {
	for _, state := range []string{"unknown", "stale", "unavailable", "enrolled"} {
		t.Run(state, func(t *testing.T) {
			h := newAudibilityHarness(t, false)
			h.control.speakerVerdict = func() SpeakerVerdict {
				v := SpeakerVerdict{Healthy: true, HasVerdict: true, Enrolled: true, At: time.Now()}
				switch state {
				case "unknown":
					v.Enrolled = false
				case "stale":
					v.At = time.Now().Add(-time.Minute)
				case "unavailable":
					v.Healthy = false
				}
				return v
			}
			h.control.verdictMaxAge = time.Second
			// Below the interruption threshold, but enough for near-miss retention.
			h.feed(.018, true, false)
			h.feed(.018, true, false)
			h.finishWithoutTrip()
			if got := h.control.sawPauseSpeechWithin(time.Second); got != (state == "enrolled") {
				t.Fatalf("pause retained=%t for %s speaker", got, state)
			}
		})
	}
}
