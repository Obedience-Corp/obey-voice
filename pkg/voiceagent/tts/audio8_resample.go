package tts

import (
	"fmt"
	"math"
)

// audio8ResampleZeroCrossings is the half-width of the interpolation kernel in
// zero crossings of the sinc. 16 puts the stopband well below the codec's own
// quantisation noise; the cost is 32 multiply-adds per output sample, paid once
// per registration.
const audio8ResampleZeroCrossings = 16

// resampleBandlimited converts mono PCM between sample rates with a
// Kaiser-windowed sinc kernel.
//
// The playback path resamples with linear interpolation (see
// audio.streamResampler and scene.ResampleLinear) because it must run inside an
// audio callback and a click at a chunk boundary is the failure that matters
// there. Registration is the opposite trade: it runs once, off the audio
// thread, and its output is a permanent description of someone's voice - the
// imaging and aliasing artifacts linear interpolation leaves behind would be
// encoded into the codes and heard in every utterance afterwards. Hence a real
// filter here rather than a shared helper with the playback path.
//
// The internal/scene resampler is not reused for the same reason a copy is not
// made of it: pkg/voiceagent/tts must not import internal/scene (see
// TestSceneHasNoAudioOrTTSDependency, which enforces the layering from the
// other side).
func resampleBandlimited(in []float32, src, dst int) ([]float32, error) {
	if src <= 0 || dst <= 0 {
		return nil, fmt.Errorf("resample %d Hz to %d Hz: sample rates must be positive", src, dst)
	}
	if len(in) == 0 {
		return nil, nil
	}
	if src == dst {
		out := make([]float32, len(in))
		copy(out, in)
		return out, nil
	}

	ratio := float64(src) / float64(dst)
	// Downsampling moves the cutoff below the output Nyquist to keep the new
	// band clean; upsampling leaves it at the input Nyquist so nothing that was
	// there is lost.
	cutoff := 0.5
	if dst < src {
		cutoff = 0.5 * float64(dst) / float64(src)
	}
	halfWidth := float64(audio8ResampleZeroCrossings) / (2 * cutoff)
	taps := int(math.Ceil(halfWidth))
	// Hoisted out of the tap loop: a 30 s 48 kHz reference evaluates the kernel
	// ~42M times, and recomputing these per tap cost more than the encoder run
	// it feeds.
	invHalfWidth := 1 / halfWidth
	twoCutoff := 2 * cutoff

	n := int(math.Round(float64(len(in)) * float64(dst) / float64(src)))
	if n <= 0 {
		return nil, nil
	}
	out := make([]float32, n)
	last := len(in) - 1
	for i := range out {
		center := float64(i) * ratio
		first := int(math.Floor(center)) - taps + 1
		var sum, norm float64
		for j := first; j <= first+2*taps-1; j++ {
			x := center - float64(j)
			w := kaiser(x * invHalfWidth)
			if w == 0 {
				continue
			}
			h := twoCutoff * sinc(twoCutoff*x) * w
			// Clamp rather than zero-pad: the edges of a reference recording
			// are the speaker's first and last syllable, and a fade to silence
			// there is an artifact the encoder would faithfully preserve.
			k := min(max(j, 0), last)
			sum += float64(in[k]) * h
			norm += h
		}
		if norm != 0 {
			sum /= norm
		}
		out[i] = float32(sum)
	}
	return out, nil
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// audio8KaiserBeta shapes the window: 8.6 puts the stopband around -65 dB,
// measured at -89 dB of alias rejection on real rate conversions.
const audio8KaiserBeta = 8.6

// audio8KaiserNorm is besselI0(beta), the window's value at x=0. It is a
// constant, and computing it inside kaiser made every one of the ~42M taps in a
// long reference run the series expansion twice instead of once.
var audio8KaiserNorm = besselI0(audio8KaiserBeta)

// kaiser is the Kaiser window over x in [-1, 1].
func kaiser(x float64) float64 {
	if x <= -1 || x >= 1 {
		return 0
	}
	return besselI0(audio8KaiserBeta*math.Sqrt(1-x*x)) / audio8KaiserNorm
}

// besselI0 is the zeroth-order modified Bessel function of the first kind, by
// its series expansion. It converges in ~30 terms for the beta used here.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for i := 1; i < 64; i++ {
		term *= (x / 2) * (x / 2) / float64(i*i)
		sum += term
		if term < sum*1e-16 {
			break
		}
	}
	return sum
}
