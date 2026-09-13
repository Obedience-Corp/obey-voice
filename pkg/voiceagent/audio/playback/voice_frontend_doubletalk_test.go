package playback

import (
	"fmt"
	"math"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Independent seeds prevent the NLMS filter from learning the near-end voice
// as a delayed copy of playback. Use the preferred 24kHz output ring latency,
// then introduce a quiet user after four seconds of echo-only adaptation.
func TestVoiceFrontendPreservesIndependentDuplexSpeech(t *testing.T) {
	const total = audio.SampleRate * 8
	const echoLag = playbackPeriods*playbackPeriodFrames*audio.SampleRate/24000 + 8*audio.SampleRate/1000
	far := farEndSpeech(total, 1)
	near := farEndSpeech(total, 99)
	scale := float32(0.02 / float64(rmsDoubletalk(near)))
	for i := range near {
		near[i] *= scale
	}

	for _, captureChunk := range []int{erleCaptureChunk, audio.SampleRate * 60 / 1000} {
		t.Run(fmt.Sprintf("%dsamples", captureChunk), func(t *testing.T) {

			run := func(withNear bool) (energy, nearDot, echoDot, nearEnergy, echoEnergy float64, count int) {
				f := audio.NewVoiceFrontend()
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

func rmsDoubletalk(samples []float32) float64 {
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	return math.Sqrt(energy / float64(len(samples)))
}
