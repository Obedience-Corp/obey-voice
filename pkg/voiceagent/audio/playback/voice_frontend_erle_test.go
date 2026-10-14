package playback

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

const (
	// erleCaptureChunk is one capture callback: miniaudio's low-latency default
	// period is 10ms, and Capture does not override it.
	erleCaptureChunk = audio.SampleRate / 100
	// erleEchoGain is how loud the speaker is at the mic.
	erleEchoGain = 0.5
)

// farEndSpeech generates a speech-like far-end signal: band-limited noise under
// a syllable-rate envelope. White noise would flatter the canceller.
func farEndSpeech(n int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	var lp float64
	for i := range out {
		lp = 0.85*lp + 0.15*rng.NormFloat64()
		env := 0.5 + 0.5*math.Sin(2*math.Pi*float64(i)/float64(audio.SampleRate)*3)
		out[i] = float32(lp * env * 0.3)
	}
	return out
}

// erleThroughFrontend runs the real push/pop cadence: each iteration pushes one
// callback of far-end audio and processes one callback of mic audio whose echo
// lags the push by echoLag samples. It reports dB of echo suppression as seen
// by VAD — measured against the same chain with no reference fed, so the NS and
// AGC stages cannot take credit for the AEC's work.
func erleThroughFrontend(t *testing.T, refDelay, echoLag int) float64 {
	t.Helper()

	const seconds = 8
	total := audio.SampleRate * seconds
	far := farEndSpeech(total+echoLag, 1)

	run := func(feedRef bool) float64 {
		f := audio.NewVoiceFrontend()
		f.SetReferenceDelay(refDelay)

		var energy float64
		for start := 0; start+erleCaptureChunk <= total; start += erleCaptureChunk {
			if feedRef {
				f.PushPlaybackReference(far[start : start+erleCaptureChunk])
			}
			mic := make([]float32, erleCaptureChunk)
			for i := range mic {
				// The mic hears what left the speaker echoLag samples ago.
				if src := start + i - echoLag; src >= 0 {
					mic[i] = erleEchoGain * far[src]
				}
			}
			out := f.ProcessCapture(mic)
			// Score the back half only, after the filter has adapted.
			if start > total/2 {
				for _, s := range out {
					energy += float64(s) * float64(s)
				}
			}
		}
		return energy
	}

	withRef := run(true)
	withoutRef := run(false)
	return 10 * math.Log10(withoutRef/math.Max(withRef, 1e-12))
}

// The shipping configuration must cancel at the latency the player actually
// creates, on every rate it will actually open. Stereo devices try the TTS
// rate first, so 24kHz is the common case, not 48kHz — and the same
// 3x512-frame ring is 64ms of output latency there against 32ms at 48kHz,
// while referenceDelaySamples under-counts by design. A suite that only
// covered 48kHz stayed green while the preferred path cancelled ~1dB.
func TestEchoCancellerERLEAcrossDeviceLatency(t *testing.T) {
	ms := func(d int) int { return d * 1000 / audio.SampleRate }
	// Thresholds sit a couple of dB under measured so platform float
	// differences do not flake, while a real regression — a dropped delay
	// offset, a shortened filter, a slower step — falls well through:
	// uncompensated, every one of these reads under 2.5dB.
	extras := []struct {
		name    string
		extraMs int
		minERLE float64
	}{
		{"ring buffer only", 0, 6.5},
		{"ring + driver latency", 8, 6},
		{"ring + driver + far speaker", 16, 5.5},
	}

	for _, rate := range []int{24000, 44100, 48000} {
		t.Run(fmt.Sprintf("%dHz", rate), func(t *testing.T) {
			refDelay := referenceDelaySamples(rate, playbackPeriodFrames)
			// The whole ring must drain before the first frame is audible.
			ring := playbackPeriods * playbackPeriodFrames * audio.SampleRate / rate

			for _, tc := range extras {
				t.Run(tc.name, func(t *testing.T) {
					echoLag := ring + tc.extraMs*audio.SampleRate/1000
					erle := erleThroughFrontend(t, refDelay, echoLag)
					t.Logf("rate %dHz: echo lag %dms, reference delay %dms, residual %dms, "+
						"%d taps (%dms) → ERLE %+.2f dB",
						rate, ms(echoLag), ms(refDelay), ms(echoLag-refDelay),
						audio.EchoCancellerTaps(), ms(audio.EchoCancellerTaps()), erle)
					if erle < tc.minERLE {
						t.Errorf("ERLE = %+.2f dB, want >= %+.2f dB — self-echo at this "+
							"latency will reach VAD and trip barge-in", erle, tc.minERLE)
					}
				})
			}
		})
	}
}

// The residual the tap window has to span is (true output latency - estimated
// delay), and the estimate is deliberately low. Lock the relationship the ERLE
// numbers depend on so a change to periods, taps, or the estimate cannot
// quietly move the worst rate past the cliff.
func TestEchoCancellerTapsCoverWorstRateResidual(t *testing.T) {
	// Driver and acoustic latency beyond the ring, in ms. The ERLE cases above
	// assert cancellation holds this far out.
	const extraMs = 16

	for _, rate := range []int{24000, 44100, 48000} {
		ring := playbackPeriods * playbackPeriodFrames * audio.SampleRate / rate
		residual := ring + extraMs*audio.SampleRate/1000 - referenceDelaySamples(rate, playbackPeriodFrames)
		if residual > audio.EchoCancellerTaps() {
			t.Errorf("rate %dHz: residual echo %dms exceeds the %dms tap window; "+
				"cancellation falls off a cliff past the last tap",
				rate, residual*1000/audio.SampleRate, audio.EchoCancellerTaps()*1000/audio.SampleRate)
		}
	}
}

// The regression itself: with no reference delay the canceller is handed
// far-end audio tens of milliseconds before its echo arrives, which no amount
// of rate alignment fixes. This is what shipped before SetReferenceDelay
// existed.
//
// Measured at 24kHz — the rate a stereo device opens at — because the
// tap window alone now covers the 48kHz profile: 768 taps span 48ms, more than
// that ring's 32ms + driver lag, so an uncompensated 48kHz run scores ~7dB and
// proves nothing. At 24kHz the ring is 64ms and the offset is still the only
// thing that brings the echo inside the window.
func TestEchoCancellerNeedsReferenceDelay(t *testing.T) {
	const rate = 24000
	// Keep the total echo path outside the filter window, while the residual
	// after the device offset fits inside it. A 72ms path no longer exposes
	// missing compensation now that the filter spans 96ms.
	echoLag := referenceDelaySamples(rate, playbackPeriodFrames) + 90*audio.SampleRate/1000

	compensated := erleThroughFrontend(t, referenceDelaySamples(rate, playbackPeriodFrames), echoLag)
	uncompensated := erleThroughFrontend(t, 0, echoLag)
	t.Logf("compensated %+.2f dB vs uncompensated %+.2f dB", compensated, uncompensated)

	if uncompensated > 3 {
		t.Fatalf("uncompensated ERLE = %+.2f dB; the test no longer reproduces the "+
			"delay bug it guards", uncompensated)
	}
	if compensated < uncompensated+4 {
		t.Errorf("reference delay bought only %+.2f dB (%.2f → %.2f); the offset is "+
			"not doing its job", compensated-uncompensated, uncompensated, compensated)
	}
}

// Barge-in only works if the user survives the canceller. A longer filter and
// a faster step both raise the risk that the AEC adapts to near-end speech and
// subtracts it away, so hold that line explicitly: when the user talks over
// playback, their voice must still dominate the front-end's output.
func TestEchoCancellerPreservesNearEndDuringDoubleTalk(t *testing.T) {
	const (
		echoLag = 40 * audio.SampleRate / 1000
		total   = audio.SampleRate * 8
	)
	refDelay := referenceDelaySamples(48000, playbackPeriodFrames)
	far := farEndSpeech(total+echoLag, 1)
	near := farEndSpeech(total, 99) // the user, uncorrelated with playback

	f := audio.NewVoiceFrontend()
	f.SetReferenceDelay(refDelay)

	var outEnergy, nearEnergy float64
	for start := 0; start+erleCaptureChunk <= total; start += erleCaptureChunk {
		f.PushPlaybackReference(far[start : start+erleCaptureChunk])
		mic := make([]float32, erleCaptureChunk)
		for i := range mic {
			if src := start + i - echoLag; src >= 0 {
				mic[i] = erleEchoGain * far[src]
			}
			// The user starts talking halfway in — the barge-in moment.
			if start > total/2 {
				mic[i] += near[start+i]
			}
		}
		out := f.ProcessCapture(mic)
		if start > total*3/4 { // score once double-talk is well established
			for i, s := range out {
				outEnergy += float64(s) * float64(s)
				nearEnergy += float64(near[start+i]) * float64(near[start+i])
			}
		}
	}

	kept := 10 * math.Log10(outEnergy/math.Max(nearEnergy, 1e-12))
	t.Logf("near-end preserved through double-talk: %+.2f dB", kept)
	if kept < 0 {
		t.Errorf("near-end preservation = %+.2f dB; the canceller is eating the "+
			"user's voice, which is exactly what barge-in needs to hear", kept)
	}
}
