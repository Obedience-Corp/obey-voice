package stt

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

type finalizableGainFrames struct {
	*scriptedFrames
	gain float64
}

func (f *finalizableGainFrames) InputGain() float64 { return f.gain }

func TestFinalizableGainReachesSTTLoops(t *testing.T) {
	for _, loop := range []string{"offline", "streaming"} {
		for _, input := range []struct {
			name string
			raw  float32
			want bool
		}{
			{"quiet_echo", .001, false},
			{"quiet_real_speech", .003, true},
		} {
			t.Run(loop+"/"+input.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				// Both cases get the same neural speech verdict. Actual STT
				// noise/admission logic must distinguish their microphone RMS
				// after seeing the same Finalizable adapter used by live input.
				frame := constFrame(1600, input.raw*6)
				all := append(append(append([]float32(nil), frame...), frame...), frame...)
				source := &finalizableGainFrames{
					scriptedFrames: &scriptedFrames{chunks: [][]float32{frame, frame, frame}},
					gain:           6,
				}
				frames := asFrameSource(audio.NewFinalizable(source))
				seg := &fakeSegmenter{speech: true, flushSeg: all}
				policy := endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond}
				var got []Event
				if loop == "offline" {
					decoded := 0
					got = runLoop(ctx, offlineLoopDeps{
						frames: frames, seg: seg, policy: policy,
						transcribe: func([]float32) (string, error) {
							decoded++
							return "retained speech", nil
						},
					})
					if (decoded != 0) != input.want {
						t.Fatalf("offline decoder calls=%d, want speech admitted=%t", decoded, input.want)
					}
				} else {
					got = runStreaming(ctx, streamingLoopDeps{
						frames: frames, seg: seg, policy: policy,
						rec: &fakeStreamingRec{partials: []string{"retained speech"}, finals: []string{"retained speech"}},
					})
					if !input.want && countPartials(got) != 0 {
						t.Fatal("gained quiet echo admitted a streaming partial")
					}
				}
				if input.want {
					final, ok := terminal(got).(FinalTranscript)
					if !ok || final.Text != "retained speech" {
						t.Fatalf("quiet real speech lost: terminal=%+v", terminal(got))
					}
				} else if _, ok := terminal(got).(Timeout); !ok {
					t.Fatalf("quiet echo admitted: terminal=%+v", terminal(got))
				}
			})
		}
	}
}

func TestFinalizableGainTracksSourceChangesAndDefaultsToOne(t *testing.T) {
	source := &finalizableGainFrames{scriptedFrames: &scriptedFrames{}, gain: 6}
	frames := asFrameSource(audio.NewFinalizable(source))
	for _, gain := range []float64{6, 3, .5, 8} {
		source.gain = gain
		if got := inputGain(frames); got != gain {
			t.Fatalf("wrapped STT input gain=%v, want current gain=%v", got, gain)
		}
	}
	if got := inputGain(asFrameSource(audio.NewFinalizable(noFrames{}))); got != 1 {
		t.Fatalf("source without gain reporting=%v, want 1", got)
	}
}
