package audio

import (
	"fmt"
	"math"
	"testing"
)

// The saved Studio Display echo-only control has 85–90ms of residual delay
// after the configured reference FIFO offset. The device-ring fixtures cover
// shorter paths, so they cannot catch a filter that excludes this echo entirely.
func TestEchoCancellerCoversMeasuredDisplayResidualDelay(t *testing.T) {
	const total = SampleRate * 8
	const residual = 90 * SampleRate / 1000
	refDelay := referenceDelaySamples(24000, playbackPeriodFrames)
	echoLag := refDelay + residual
	far := farEndSpeech(total, 7)

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
							mic[i] = erleEchoGain * far[src]
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
