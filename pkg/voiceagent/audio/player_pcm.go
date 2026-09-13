package audio

import (
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
	return resampleLinear(samples, from, to)
}

func resampleLinear(samples []float32, from, to int) []float32 {
	if len(samples) == 0 || from <= 0 || to <= 0 || from == to {
		return samples
	}

	outLen := int(math.Round(float64(len(samples)) * float64(to) / float64(from)))
	if outLen < 1 {
		outLen = 1
	}

	out := make([]float32, outLen)
	step := float64(from) / float64(to)
	for i := range outLen {
		pos := float64(i) * step
		index := int(pos)
		if index >= len(samples)-1 {
			out[i] = samples[len(samples)-1]
			continue
		}

		frac := float32(pos - float64(index))
		a := samples[index]
		b := samples[index+1]
		out[i] = a + (b-a)*frac
	}

	return out
}
