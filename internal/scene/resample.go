package scene

import (
	"fmt"
	"math"
)

// Resampler converts mono PCM from one sample rate to another. It is a
// dependency rather than an import so internal/scene stays cgo-free: the
// command layer supplies the implementation, and the integration binary can
// compile the same code without the audio stack behind it.
type Resampler func(in []float32, src, dst int) ([]float32, error)

// ResampleLinear resamples mono PCM by linear interpolation. It is the
// production Resampler.
//
// A scene needs one because neither mouth will convert for us: Kokoro and Qwen
// both reject a SynthesisRequest.SampleRate that is not their native rate, and
// which rates a run actually sees depends on the personas it was pointed at.
// Identity returns the input slice itself, so the common single-rate cast pays
// nothing; callers must not mutate what they pass in.
func ResampleLinear(in []float32, src, dst int) ([]float32, error) {
	if src <= 0 || dst <= 0 {
		return nil, fmt.Errorf("scene: resample %d Hz to %d Hz: sample rates must be positive", src, dst)
	}
	if src == dst || len(in) == 0 {
		return in, nil
	}
	// Rounded, not truncated: a truncated length loses up to a full sample of
	// audio per turn, which is a drift the manifest's integer clocks then
	// carry into the video step.
	n := int(math.Round(float64(len(in)) * float64(dst) / float64(src)))
	if n <= 0 {
		return nil, nil
	}
	out := make([]float32, n)
	ratio := float64(src) / float64(dst)
	last := len(in) - 1
	for i := range out {
		pos := float64(i) * ratio
		left := int(pos)
		if left >= last {
			out[i] = in[last]
			continue
		}
		frac := float32(pos - float64(left))
		out[i] = in[left] + (in[left+1]-in[left])*frac
	}
	return out, nil
}
