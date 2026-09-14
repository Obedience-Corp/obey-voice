package stt

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// stationaryTailVAD learns the initial room window as neural-false, then
// latches neural-true through the utterance and its stationary tail. It never
// supplies an endpoint itself: only the loop's silence policy can flush it.
// Clear discards actual PCM, exposing destructive resets after admission.
type stationaryTailVAD struct {
	frames int
	pcm    []float32
	start  int
	queued bool
}

func (v *stationaryTailVAD) AcceptWaveform(samples []float32) {
	v.frames++
	if v.IsSpeech() && v.start < 0 {
		v.start = len(v.pcm)
	}
	v.pcm = append(v.pcm, samples...)
}
func (v *stationaryTailVAD) IsSpeech() bool         { return v.frames > gateHist }
func (v *stationaryTailVAD) IsSpeechDetected() bool { return v.queued }
func (v *stationaryTailVAD) IsEmpty() bool          { return !v.queued }
func (v *stationaryTailVAD) FrontSegment() ([]float32, int) {
	return v.pcm[v.start:], v.start
}
func (v *stationaryTailVAD) Pop() { v.queued = false }
func (v *stationaryTailVAD) Clear() {
	v.pcm = nil
	v.start = -1
	v.queued = false
}
func (v *stationaryTailVAD) Flush() { v.queued = v.start >= 0 }

// This recognizer stores exactly the context still available at finalization.
// It neither invents a neural vote nor supplies a premature provider endpoint.
type stationaryTailRecognizer struct {
	pcm      []float32
	finalPCM []float32
	finals   int
}

func (r *stationaryTailRecognizer) Accept(samples []float32) { r.pcm = append(r.pcm, samples...) }
func (*stationaryTailRecognizer) Partial() string            { return "" }
func (*stationaryTailRecognizer) IsEndpoint() bool           { return false }
func (r *stationaryTailRecognizer) Finalize() string {
	r.finals++
	r.finalPCM = slices.Clone(r.pcm)
	return "complete utterance"
}
func (r *stationaryTailRecognizer) Reset() error { r.pcm = nil; return nil }

func TestStationaryTailPreservesAdmittedSpeechUntilSilenceEndpoint(t *testing.T) {
	for _, loop := range []string{"offline", "streaming"} {
		for _, resume := range []bool{false, true} {
			name := "tail"
			if resume {
				name = "pause_then_more_speech"
			}
			t.Run(loop+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var chunks [][]float32
				appendFrames := func(n int, amplitude float32) {
					for range n {
						chunks = append(chunks, constFrame(1600, amplitude))
					}
				}
				appendFrames(gateHist, .01)
				for _, amplitude := range []float32{.05, .025, .08, .035, .06, .03} {
					appendFrames(1, amplitude)
				}
				if resume {
					// Nine tail frames settle the modulation window; the next
					// three are rejected as stationary (300ms of policy silence).
					// Neither first rejection nor this short run ends the turn.
					appendFrames(12, .01)
					for _, amplitude := range []float32{.07, .04, .09, .055, .075, .03} {
						appendFrames(1, amplitude)
					}
				}
				// Ten identical frames produce the first stationary rejection;
				// eleven more reach exactly the configured 1.2s silence endpoint.
				appendFrames(21, .01)
				endpointFrames := len(chunks)
				var want []float32
				start := 0
				if loop == "offline" {
					// Production pre-roll includes 300ms before the neural onset.
					start = gateHist - 3
				}
				for _, frame := range chunks[start:] {
					want = append(want, frame...)
				}
				// This later phrase and EOF must remain unread: finalization is
				// from MinSilence, not fixture exhaustion or a detector endpoint.
				appendFrames(3, .1)
				frames := &scriptedFrames{chunks: chunks}
				seg := newVADSegmenter(&stationaryTailVAD{start: -1}, preRollSamplesFromMS(300))
				policy := endpoint.Policy{MinSpeech: 200 * time.Millisecond, MinSilence: 1200 * time.Millisecond, FinalizeOnEOF: true}
				var got []Event
				var retained []float32
				finals := 0
				if loop == "offline" {
					got = runLoop(ctx, offlineLoopDeps{frames: frames, seg: seg, policy: policy, transcribe: func(samples []float32) (string, error) {
						finals++
						retained = slices.Clone(samples)
						return "complete utterance", nil
					}})
				} else {
					rec := &stationaryTailRecognizer{}
					got = runStreaming(ctx, streamingLoopDeps{frames: frames, seg: seg, rec: rec, policy: policy})
					retained, finals = rec.finalPCM, rec.finals
				}
				if final, ok := terminal(got).(FinalTranscript); !ok || final.Text != "complete utterance" {
					t.Errorf("terminal = %+v, want complete utterance", terminal(got))
				}
				if frames.i != endpointFrames || frames.done {
					t.Errorf("consumed %d frames (EOF=%v), want silence endpoint at %d before next phrase", frames.i, frames.done, endpointFrames)
				}
				if finals != 1 || !slices.Equal(retained, want) {
					t.Errorf("finalizations=%d retained samples=%d, want one with exact %d PCM samples including all speech", finals, len(retained), len(want))
				}
			})
		}
	}
}
