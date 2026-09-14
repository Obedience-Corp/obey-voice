package stt

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// gainJumpOnsetVAD delays its neural speech mark until after the three onset
// frames. It retains actual PCM and trims the unconfirmed prefix from its
// segment, so the production vadSegmenter's pre-roll must recover those bytes.
// Clear truly deletes buffered input: it cannot hide a loop's destructive reset.
type gainJumpOnsetVAD struct {
	pcm    []float32
	start  int
	speech bool
	queued bool
}

func (v *gainJumpOnsetVAD) AcceptWaveform(samples []float32) {
	v.speech = len(samples) > 0 && samples[0] >= .02
	if v.speech && v.start < 0 {
		v.start = len(v.pcm)
	}
	v.pcm = append(v.pcm, samples...)
}
func (v *gainJumpOnsetVAD) IsSpeech() bool         { return v.speech }
func (v *gainJumpOnsetVAD) IsSpeechDetected() bool { return v.queued }
func (v *gainJumpOnsetVAD) IsEmpty() bool          { return !v.queued }
func (v *gainJumpOnsetVAD) FrontSegment() ([]float32, int) {
	return v.pcm[v.start:], v.start
}
func (v *gainJumpOnsetVAD) Pop() { v.queued = false }
func (v *gainJumpOnsetVAD) Clear() {
	v.pcm = nil
	v.start = -1
	v.speech, v.queued = false, false
}
func (v *gainJumpOnsetVAD) Flush() { v.queued = v.start >= 0 }

// Streaming resets discard decoder context too. Recording the actual Accept
// payload after the last Reset exposes onset loss without model inference.
type gainJumpOnsetRecognizer struct{ pcm []float32 }

func (r *gainJumpOnsetRecognizer) Accept(samples []float32) { r.pcm = append(r.pcm, samples...) }
func (*gainJumpOnsetRecognizer) Partial() string            { return "" }
func (*gainJumpOnsetRecognizer) IsEndpoint() bool           { return false }
func (*gainJumpOnsetRecognizer) Finalize() string           { return "preserved speech" }
func (r *gainJumpOnsetRecognizer) Reset() error             { r.pcm = nil; return nil }

func TestGainJumpPendingNeuralOnsetPreservesSTTInput(t *testing.T) {
	for _, loop := range []string{"offline", "streaming"} {
		t.Run(loop, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var chunks [][]float32
			var room, speech []float32
			for range gateHist {
				frame := constFrame(1600, .001)
				chunks = append(chunks, frame)
				room = append(room, frame...)
			}
			// Distinct 100ms onset frames remain neural-false while exceeding
			// the learned envelope by >8x. The third re-anchors the floor.
			// Later neural-confirmed speech must retain all three onset frames.
			for _, amplitude := range []float32{.012, .014, .016, .06, .05, .07} {
				frame := constFrame(1600, amplitude)
				chunks = append(chunks, frame)
				speech = append(speech, frame...)
			}
			detector := &gainJumpOnsetVAD{start: -1}
			seg := newVADSegmenter(detector, preRollSamplesFromMS(300))
			frames := &scriptedFrames{chunks: chunks}
			policy := endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond}
			var got []Event
			var retained, want []float32
			if loop == "offline" {
				want = speech
				got = runLoop(ctx, offlineLoopDeps{
					frames: frames, seg: seg, policy: policy,
					transcribe: func(samples []float32) (string, error) {
						retained = append([]float32(nil), samples...)
						return "preserved speech", nil
					},
				})
			} else {
				rec := &gainJumpOnsetRecognizer{}
				got = runStreaming(ctx, streamingLoopDeps{frames: frames, seg: seg, rec: rec, policy: policy})
				retained = rec.pcm
				// Streaming accepts pre-speech context as well as the utterance.
				want = append(append([]float32(nil), room...), speech...)
			}
			if final, ok := terminal(got).(FinalTranscript); !ok || final.Text != "preserved speech" {
				t.Fatalf("terminal event=%+v, want final transcript", terminal(got))
			}
			if !slices.Equal(retained, want) {
				t.Fatalf("gain re-anchor lost or changed STT onset: retained samples=%d, want exact PCM=%d (including 4800 onset samples)", len(retained), len(want))
			}
		})
	}
}
