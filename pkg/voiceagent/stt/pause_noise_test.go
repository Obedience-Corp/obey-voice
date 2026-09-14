package stt

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type pauseNoiseFrame struct {
	frame     audio.Frame
	processed chan struct{}
}

// Acknowledge a frame on the following read, after the loop has completed its
// admission/permit decision. An AcceptWaveform acknowledgement is too early.
type pauseNoiseSource struct {
	in       chan pauseNoiseFrame
	previous chan struct{}
}

func (s *pauseNoiseSource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if s.previous != nil {
		close(s.previous)
		s.previous = nil
	}
	select {
	case next := <-s.in:
		s.previous = next.processed
		return next.frame, nil
	case <-ctx.Done():
		return audio.Frame{}, ctx.Err()
	case <-time.After(time.Millisecond):
		return audio.Frame{}, audio.ErrNoFrameReady
	}
}
func (*pauseNoiseSource) Close() error       { return nil }
func (*pauseNoiseSource) InputGain() float64 { return 6 }

// The neural detector queues its first, quiet frame as a false positive.
// Subsequent real speech remains in progress until Flush, as on a live source.
type pauseNoiseSegmenter struct {
	fakeSegmenter
	retained []float32
	resets   int
}

func (s *pauseNoiseSegmenter) AcceptWaveform(samples []float32) {
	s.fakeSegmenter.AcceptWaveform(samples)
	s.speech = true
	if s.accepts == 1 {
		s.segments = append(s.segments, slices.Clone(samples))
		return
	}
	s.retained = append(s.retained, samples...)
	s.flushSeg = s.retained
}
func (s *pauseNoiseSegmenter) Reset() { s.resets++ }

func TestPauseIgnoresQueuedFalsePositiveAfterYield(t *testing.T) {
	for _, loop := range []string{"offline", "streaming"} {
		t.Run(loop, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			scope := pauseTestScope()
			pause := newPauseController("noise-" + loop)
			pause.BindCompletionCoordinator(work.NewCoordinator(scope))
			raw := &pauseNoiseSource{in: make(chan pauseNoiseFrame, 1)}
			frames := audio.NewFinalizable(raw)
			seg := &pauseNoiseSegmenter{}
			out := make(chan Event, 16)
			done := make(chan struct{})
			var expected []float32
			for i := range 3 {
				frame := constFrame(1600, .003*6)
				frame[0] += float32(i) / 1000
				expected = append(expected, frame...)
			}
			go func() {
				defer close(done)
				policy := endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond}
				if loop == "offline" {
					runOfflineLoop(ctx, offlineLoopDeps{frames: frames, seg: seg, pause: pause, policy: policy, transcribe: func(samples []float32) (string, error) {
						if !slices.Equal(samples, expected) {
							return "", fmt.Errorf("decoder received rejected noise or lost real onset: %d samples", len(samples))
						}
						return "retained speech", nil
					}}, out)
				} else {
					runStreamingLoop(ctx, streamingLoopDeps{frames: frames, seg: seg, pause: pause, policy: policy, rec: &fakeStreamingRec{partials: []string{"retained speech"}, finals: []string{"retained speech"}}}, out)
				}
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("STT loop did not join")
				}
			})
			waitForEvent(t, out, func(e Event) bool { p, ok := e.(PhaseEvent); return ok && p.Phase == "listening" })
			result, err := pause.request(ctx, scope, "quiet-after-yield")
			if err != nil || result.Decision != PauseAcknowledged {
				t.Fatalf("initial pause = %+v, %v", result, err)
			}
			waitForEvent(t, out, func(e Event) bool { _, ok := e.(YieldedForCompletion); return ok })
			feed := func(samples []float32) {
				t.Helper()
				processed := make(chan struct{})
				raw.in <- pauseNoiseFrame{frame: audio.Frame{Samples: samples, SampleRate: audio.SampleRate, Channels: 1, SourceKind: audio.SourceLive}, processed: processed}
				select {
				case <-processed:
				case <-done:
					t.Fatal("STT finalized before the real utterance ended")
				case <-ctx.Done():
					t.Fatal("STT did not finish its frame decision")
				}
			}
			feed(constFrame(1600, .001*6))
			if !result.Token.Permit.Valid() {
				t.Fatal("queued inaudible neural segment revoked completion")
			}
			if seg.HasSegments() {
				t.Fatal("rejected queued noise could finalize the next real utterance")
			}
			for start := 0; start < len(expected); start += 1600 {
				feed(expected[start : start+1600])
			}
			if result.Token.Permit.Valid() {
				t.Fatal("real speech did not revoke completion")
			}
			if seg.resets != 1 || !slices.Equal(seg.retained, expected) {
				t.Fatalf("real onset changed after noise rejection: resets=%d samples=%d", seg.resets, len(seg.retained))
			}
			if err := pause.resume(ctx, result.Token); err != nil {
				t.Fatal(err)
			}
			raw.in <- pauseNoiseFrame{frame: audio.Frame{Final: true, SourceKind: audio.SourceFixture}}
			waitForEvent(t, out, func(e Event) bool { final, ok := e.(FinalTranscript); return ok && final.Text == "retained speech" })
		})
	}
}
