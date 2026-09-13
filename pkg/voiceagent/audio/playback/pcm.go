package playback

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"math"
)

func clearBytes(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
}

func float32ToPCM16(samples []float32) []int16 {
	pcm := make([]int16, len(samples))
	for i, sample := range samples {
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}
		pcm[i] = int16(sample * float32(math.MaxInt16))
	}
	return pcm
}

// resample converts mono PCM between sample rates for playback. Prefer calling
// this on a complete utterance (see finalizeSegment).
//
// Linear interpolation only: Catmull-Rom cubic was tried for 24→48 and sounded
// worse on Studio Display Speakers because overshoot hard-clips in
// float32ToPCM16 and produces crackle on peaks. Linear never overshoots the
// local sample pair.
func resample(samples []float32, from, to int) []float32 {
	return audio.ResampleLinear(samples, from, to)
}
