package audio

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// The saved Studio Display echo-only control has 85–90ms of residual delay
// after the configured reference FIFO offset. The device-ring fixtures cover
// shorter paths, so they cannot catch a filter that excludes this echo entirely.
func TestEchoCancellerCoversMeasuredDisplayResidualDelay(t *testing.T) {
	const total = SampleRate * 8
	const residual = 90 * SampleRate / 1000
	// Recorded reference FIFO offset; playback package tests separately cover
	// how each device configuration derives its offset.
	const refDelay = 682
	echoLag := refDelay + residual
	far := delayedEchoFixture(total, 7)

	for _, chunkSize := range []int{SampleRate / 100, SampleRate * 60 / 1000} {
		t.Run(fmt.Sprintf("%dsamples", chunkSize), func(t *testing.T) {
			run := func(cancelEcho bool) float64 {
				f := NewVoiceFrontend()
				f.SetReferenceDelay(refDelay)
				if !cancelEcho {
					// Keep the identical reference, NS, and AGC paths so their
					// duplex attenuation cannot masquerade as echo cancellation.
					f.aec = nlmsEchoCanceller{}
				}
				var energy float64
				for start := 0; start+chunkSize <= total; start += chunkSize {
					f.PushPlaybackReference(far[start : start+chunkSize])
					mic := make([]float32, chunkSize)
					for i := range mic {
						if src := start + i - echoLag; src >= 0 {
							mic[i] = 0.5 * far[src]
						}
					}
					out := f.ProcessCapture(mic)
					if start < total/2 {
						continue
					}
					for _, sample := range out {
						energy += float64(sample) * float64(sample)
					}
				}
				return energy
			}
			withAEC, withoutAEC := run(true), run(false)
			erle := 10 * math.Log10(withoutAEC/math.Max(withAEC, 1e-12))
			t.Logf("reference delay %.3fms + measured residual 90ms, %d taps: ERLE %+.2fdB", float64(refDelay)*1000/SampleRate, echoCancellerTaps, erle)
			// Require the adaptive filter to remove at least half the echo
			// energy, beyond the suppression already supplied by NS/AGC.
			if withAEC >= withoutAEC/2 {
				t.Errorf("delayed echo ERLE %+.2fdB: AEC did not halve the residual echo energy", erle)
			}
		})
	}
}

// Band-limited noise under a syllable envelope avoids a white-noise fixture
// that makes adaptive cancellation unrealistically easy.
func delayedEchoFixture(n int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	var lowPass float64
	for i := range out {
		lowPass = 0.85*lowPass + 0.15*rng.NormFloat64()
		envelope := 0.5 + 0.5*math.Sin(2*math.Pi*float64(i)/SampleRate*3)
		out[i] = float32(lowPass * envelope * 0.3)
	}
	return out
}
