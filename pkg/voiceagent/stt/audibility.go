package stt

import "math"

// SpeechAudibility applies STT's utterance audibility check to gained audio.
// Its zero value is ready to use. A single consumer must own it and call Reset
// when beginning a new utterance or neural speech run.
//
// A completed 100ms window above STT's microphone floor latches the verdict.
// A positive trailing window can also make Audible return true, but that
// temporary verdict is not latched unless a completed window clears the floor.
type SpeechAudibility struct {
	tracker audibleTracker
}

// Observe folds samples into the current utterance and returns its audibility.
// gain is the amplification already applied to this callback's samples. Each
// callback is normalized independently, so a later gain change cannot alter the
// meaning of earlier samples. Gains at or below one, and NaN, follow speechFloor
// semantics and leave the samples at their supplied level.
//
// Observe neither modifies samples nor allocates storage. It uses the existing
// audibleTracker and its thresholds rather than requiring consecutive loud
// callbacks, which would reject words separated by quieter speech.
func (a *SpeechAudibility) Observe(samples []float32, gain float64) bool {
	if gain <= 1 || math.IsNaN(gain) {
		a.tracker.push(samples)
		return a.Audible()
	}

	var normalized [audibleWindow]float32
	for len(samples) > 0 {
		n := min(len(samples), len(normalized))
		for i, sample := range samples[:n] {
			normalized[i] = float32(float64(sample) / gain)
		}
		a.tracker.push(normalized[:n])
		samples = samples[n:]
	}
	return a.Audible()
}

// Audible reports the current utterance verdict without consuming more audio.
func (a *SpeechAudibility) Audible() bool { return a.tracker.audible() }

// Reset discards the previous utterance, including its retained trailing window.
func (a *SpeechAudibility) Reset() { a.tracker.reset() }
