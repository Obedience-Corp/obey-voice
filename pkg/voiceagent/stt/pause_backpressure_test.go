package stt

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// retainingSegmenter records the samples accepted by the production loop's
// segmenter seam. It makes this test prove that the onset reached STT while
// the advisory event stream was full, rather than merely observing controller
// state from another goroutine.
type retainingSegmenter struct {
	fakeSegmenter
	mu       sync.Mutex
	accepted [][]float32
}

func (s *retainingSegmenter) AcceptWaveform(samples []float32) {
	s.mu.Lock()
	s.accepted = append(s.accepted, append([]float32(nil), samples...))
	s.mu.Unlock()
	s.fakeSegmenter.AcceptWaveform(samples)
}

func (s *retainingSegmenter) acceptedFrames() [][]float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := make([][]float32, len(s.accepted))
	for i := range s.accepted {
		frames[i] = append([]float32(nil), s.accepted[i]...)
	}
	return frames
}

func TestPauseBackpressureDoesNotHideRetainedOnset(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *pauseController, audio.FrameSource, *retainingSegmenter, chan<- Event)
	}{
		{name: "offline", run: func(ctx context.Context, pause *pauseController, frames audio.FrameSource, seg *retainingSegmenter, events chan<- Event) {
			runOfflineLoop(ctx, offlineLoopDeps{
				frames: frames, seg: seg, pause: pause, policy: endpoint.Policy{StartTimeout: time.Second},
				transcribe: func([]float32) (string, error) { return "", nil },
			}, events)
		}},
		{name: "streaming", run: func(ctx context.Context, pause *pauseController, frames audio.FrameSource, seg *retainingSegmenter, events chan<- Event) {
			runStreamingLoop(ctx, streamingLoopDeps{
				frames: frames, seg: seg, rec: &fakeStreamingRec{partials: []string{"echo"}},
				pause: pause, policy: endpoint.Policy{StartTimeout: time.Second},
			}, events)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := pauseTestScope()
			pause := newPauseController("backpressure-" + tc.name)
			pause.BindCompletionCoordinator(work.NewCoordinator(scope))
			frames := &barrierFrames{in: make(chan audio.Frame, 3)}
			seg := &retainingSegmenter{fakeSegmenter: fakeSegmenter{speech: true}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			events := make(chan Event, 8)
			go func() {
				tc.run(ctx, pause, frames, seg, events)
				close(done)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("STT loop did not stop")
				}
			}()

			// Consume only the reliable initial readiness event. Then obtain the
			// reliable yield event before deliberately stopping event consumption.
			waitForEvent(t, events, func(event Event) bool {
				phase, ok := event.(PhaseEvent)
				return ok && phase.Phase == "listening"
			})
			requestCtx, requestCancel := context.WithTimeout(ctx, time.Second)
			result, err := pause.request(requestCtx, scope, "backpressure")
			requestCancel()
			if err != nil || result.Decision != PauseAcknowledged {
				t.Fatalf("pause = %+v, %v", result, err)
			}
			waitForEvent(t, events, func(event Event) bool {
				yielded, ok := event.(YieldedForCompletion)
				return ok && yielded.RequestID == "backpressure"
			})

			// A real consumer can stop after YieldedForCompletion. Eight advisory
			// events fill the production-sized channel without touching the
			// reliable final/yield/failure delivery paths.
			for i := 0; i < cap(events); i++ {
				events <- PartialTranscript{Text: "queued"}
			}
			speech := speechFrame()
			for i := 0; i < 3; i++ {
				frame := append([]float32(nil), speech...)
				frame[0] = speech[0] + float32(i)/100
				frames.in <- audio.Frame{Samples: frame, SampleRate: audio.SampleRate, Channels: 1, SourceKind: audio.SourceLive}
			}

			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for result.Token.Permit.Valid() {
				select {
				case <-deadline.C:
					t.Fatal("speech did not revoke permit behind a full advisory channel")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			framesDeadline := time.NewTimer(time.Second)
			defer framesDeadline.Stop()
			var accepted [][]float32
			for {
				accepted = seg.acceptedFrames()
				if len(accepted) >= 3 {
					break
				}
				select {
				case <-framesDeadline.C:
					t.Fatalf("retained onset frames = %d, want 3", len(accepted))
				default:
					time.Sleep(time.Millisecond)
				}
			}
			for i := range accepted {
				want := append([]float32(nil), speech...)
				want[0] += float32(i) / 100
				if !slices.Equal(accepted[i], want) {
					t.Fatalf("retained frame %d changed or lost samples", i)
				}
			}
		})
	}
}

func waitForEvent(t *testing.T, events <-chan Event, match func(Event) bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for STT event")
		}
	}
}
