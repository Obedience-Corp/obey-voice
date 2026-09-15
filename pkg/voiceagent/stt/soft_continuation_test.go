package stt

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// The recognizer produces its first partial only during the soft remainder.
// This verifies streaming partials use continuation, without allowing partials
// from an entirely below-admission-floor recording to start a turn.
type softContinuationRecognizer struct {
	stationaryTailRecognizer
	partialAfter int
}

func (r *softContinuationRecognizer) Partial() string {
	if len(r.pcm) >= r.partialAfter {
		return "including the softer ending"
	}
	return ""
}

func TestSoftContinuationPreservesUtteranceAndRejectsNoiseTails(t *testing.T) {
	for _, loop := range []string{"offline", "streaming"} {
		for _, gain := range []float64{1, 6} {
			for _, scenario := range []string{"soft_remainder", "stationary_quiet_tail", "unlearned_stationary_tail", "near_silent_tail", "quiet_only"} {
				t.Run(fmt.Sprintf("%s/gain%g/%s", loop, gain, scenario), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					var chunks [][]float32
					appendFrames := func(n int, raw float32) {
						for range n {
							chunks = append(chunks, constFrame(audio.ChunkSize, raw*float32(gain)))
						}
					}
					// Ten neural-false frames establish a measured room floor.
					room := float32(.001)
					if scenario == "unlearned_stationary_tail" {
						room = 0 // a muted/quiet startup provides no measured floor
					}
					appendFrames(gateHist, room)
					if scenario != "quiet_only" {
						for _, raw := range []float32{.005, .004, .006, .0045, .007, .0055} {
							appendFrames(1, raw)
						}
					}
					onsetEnd := len(chunks)
					switch scenario {
					case "soft_remainder", "quiet_only":
						// 2.4s of modulated neural speech, all below the admission
						// floor: longer than the 1.2s endpoint that used to cut it.
						for range 4 {
							for _, raw := range []float32{.0007, .0015, .0008, .0017, .0006, .0013} {
								appendFrames(1, raw)
							}
						}
						appendFrames(12, 0)
					case "stationary_quiet_tail", "unlearned_stationary_tail":
						// The same neural latch over stationary room noise must
						// stop extending the admitted utterance after settling.
						appendFrames(30, .001)
					case "near_silent_tail":
						// Nonzero noise remains below the existing digital-silence
						// bound even after gain; it cannot prolong the turn.
						appendFrames(12, gateSilent/2)
					}
					end := len(chunks)
					if scenario != "quiet_only" {
						// A later loud phrase and EOF must remain unread. The
						// desired endpoint is trailing silence, not fixture EOF.
						appendFrames(3, .02)
					}
					frames := &finalizableGainFrames{scriptedFrames: &scriptedFrames{chunks: chunks}, gain: gain}
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
						rec := &softContinuationRecognizer{partialAfter: (onsetEnd + 12) * audio.ChunkSize}
						got = runStreaming(ctx, streamingLoopDeps{frames: frames, seg: seg, policy: policy, rec: rec})
						retained, finals = rec.finalPCM, rec.finals
					}
					if scenario == "quiet_only" {
						if _, ok := terminal(got).(Timeout); !ok || finals != 0 || countPartials(got) != 0 {
							t.Fatalf("quiet input admitted: terminal=%+v finals=%d partials=%d", terminal(got), finals, countPartials(got))
						}
						return
					}
					if final, ok := terminal(got).(FinalTranscript); !ok || final.Text != "complete utterance" {
						t.Fatalf("terminal=%+v, want complete utterance", terminal(got))
					}
					if scenario == "stationary_quiet_tail" || scenario == "unlearned_stationary_tail" {
						if frames.i > onsetEnd+gateHist+11 || frames.i <= onsetEnd {
							t.Fatalf("stationary tail did not end within its settling window plus 1.2s: consumed=%d onsetEnd=%d", frames.i, onsetEnd)
						}
					} else if frames.i != end {
						t.Fatalf("consumed=%d frames, want exactly %d including complete remainder and silence", frames.i, end)
					}
					if frames.done {
						t.Fatal("endpoint waited for EOF instead of ending on silence")
					}
					start := 0
					if loop == "offline" {
						start = gateHist - 3 // the real segmenter's 300ms pre-roll
					}
					var want []float32
					for _, chunk := range chunks[start:frames.i] {
						want = append(want, chunk...)
					}
					if finals != 1 || !slices.Equal(retained, want) {
						t.Fatalf("finals=%d retained=%d, want one exact %d-sample utterance", finals, len(retained), len(want))
					}
					if loop == "streaming" && scenario == "soft_remainder" && countPartials(got) == 0 {
						t.Fatal("streaming dropped the soft remainder's partial transcript")
					}
				})
			}
		}
	}
}
