package audio

import (
	"fmt"
	"math"
	"testing"
)

// Independent seeds prevent the NLMS filter from learning the near-end voice
// as a delayed copy of playback. Use the preferred 24kHz output ring latency,
// then introduce a quiet user after four seconds of echo-only adaptation.
func TestVoiceFrontendPreservesIndependentDuplexSpeech(t *testing.T) {
	const total = SampleRate * 8
	const echoLag = playbackPeriods*playbackPeriodFrames*SampleRate/24000 + 8*SampleRate/1000
	far := farEndSpeech(total, 1)
	near := farEndSpeech(total, 99)
	scale := float32(0.02 / float64(frameRMS(near)))
	for i := range near {
		near[i] *= scale
	}

	for _, captureChunk := range []int{erleCaptureChunk, SampleRate * 60 / 1000} {
		t.Run(fmt.Sprintf("%dsamples", captureChunk), func(t *testing.T) {

			run := func(withNear bool) (energy, nearDot, echoDot, nearEnergy, echoEnergy float64, count int) {
				f := NewVoiceFrontend()
				f.SetReferenceDelay(referenceDelaySamples(24000, playbackPeriodFrames))
				for start := 0; start+captureChunk <= total; start += captureChunk {
					f.PushPlaybackReference(far[start : start+captureChunk])
					mic := make([]float32, captureChunk)
					for i := range mic {
						if src := start + i - echoLag; src >= 0 {
							mic[i] = erleEchoGain * far[src]
						}
						if withNear && start >= total/2 {
							mic[i] += near[start+i]
						}
					}
					out := f.ProcessCapture(mic)
					if start < total*3/4 {
						continue
					}
					for i, sample := range out {
						s, n, e := float64(sample), float64(near[start+i]), float64(erleEchoGain*far[start+i-echoLag])
						energy += s * s
						nearDot += s * n
						echoDot += s * e
						nearEnergy += n * n
						echoEnergy += e * e
						count++
					}
				}
				return
			}
			energy, nearDot, echoDot, nearEnergy, echoEnergy, count := run(true)
			controlEnergy, _, _, _, _, _ := run(false)
			rms := math.Sqrt(energy / float64(count))
			controlRMS := math.Sqrt(controlEnergy / float64(count))
			nearCorrelation := nearDot / math.Sqrt(energy*nearEnergy)
			echoCorrelation := echoDot / math.Sqrt(energy*echoEnergy)
			nearGain := nearDot / nearEnergy
			t.Logf("near RMS=0.020, output RMS=%.5f, echo-only RMS=%.5f, near correlation=%.3f, echo correlation=%.3f, near projection gain=%.3f", rms, controlRMS, nearCorrelation, echoCorrelation, nearGain)
			// This synthetic check establishes that an independent user contributes
			// more than residual echo. It does not establish intelligibility or
			// physical interruption; saved-trace replay and physical controls test
			// the detector and the complete interruption path separately.
			if nearCorrelation <= math.Abs(echoCorrelation) || nearGain <= 0 {
				t.Errorf("independent near-end contribution does not dominate echo")
			}
			if rms <= controlRMS {
				t.Errorf("output RMS=%.5f, want greater than echo-only %.5f", rms, controlRMS)
			}
		})
	}
}

// Quiet samples between speech peaks carry waveform structure. Duplex NS may
// attenuate the frame, but must not gate those samples again after attenuation.
func TestDuplexNoiseSuppressionPreservesWaveform(t *testing.T) {
	for _, size := range []int{erleCaptureChunk, SampleRate * 60 / 1000} {
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
