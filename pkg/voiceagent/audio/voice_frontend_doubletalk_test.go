package audio

import (
	"fmt"
	"math"
	"testing"
)

// Quiet samples between speech peaks carry waveform structure. Duplex NS may
// attenuate the frame, but must not gate those samples again after attenuation.
func TestDuplexNoiseSuppressionPreservesWaveform(t *testing.T) {
	for _, size := range []int{SampleRate / 100, SampleRate * 60 / 1000} {
		t.Run(fmt.Sprintf("%dsamples", size), func(t *testing.T) {
			original := make([]float32, size)
			for i := range original {
				// A voiced frame with both quiet detail and stronger speech peaks.
				original[i] = float32(0.065*math.Sin(2*math.Pi*float64(i)/37) + 0.018*math.Sin(2*math.Pi*float64(i)/11))
			}
			output := append([]float32(nil), original...)
			ns := newNoiseSuppressor(initialNoiseFloor, noiseSuppressorTarget)
			ns.Process(output, true)
			var dot, before, after float64
			for i, s := range original {
				a, b := float64(s), float64(output[i])
				dot += a * b
				before += a * a
				after += b * b
			}
			gain := dot / before
			if gain <= 0 || gain >= 1 {
				t.Fatalf("duplex NS must attenuate the frame: gain=%.6f", gain)
			}
			var distortion float64
			for i, s := range original {
				d := float64(output[i]) - gain*float64(s)
				distortion += d * d
			}
			// Float32 multiplication has rounding error; any material residual means
			// the stage changed waveform shape instead of applying uniform gain.
			if relative := math.Sqrt(distortion / after); relative > 1e-6 {
				t.Errorf("duplex waveform distortion %.6f, want only float32 rounding", relative)
			}
		})
	}
}
