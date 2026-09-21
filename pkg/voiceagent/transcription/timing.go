package transcription

import (
	"fmt"
	"math"
	"math/bits"
)

const microsPerSecond = 1_000_000

// maxTimelineSamples is the largest sample count whose microsecond conversion
// still fits in an int64. At 16 kHz it is far longer than any recording, but a
// corrupt header can claim anything, so the runner refuses it rather than
// wrapping around.
const maxTimelineSamples = math.MaxInt64 / microsPerSecond

// SamplesToMicros converts a position on the timeline to integer microseconds,
// rounding down. It takes the rate explicitly rather than a package-level
// capture rate, and stays in integers instead of round-tripping through
// time.Duration.
func SamplesToMicros(samples int64, rate int) int64 {
	if rate <= 0 || samples <= 0 {
		return 0
	}
	return mulDivMicros(samples, int64(rate), false)
}

// DurationMicros converts a length in samples to integer microseconds, rounding
// up. Ends round up so a cue covers every sample it contains; starts round
// down. Both a span's end and the file's duration use this, so an end that
// reaches the last sample equals the reported duration instead of landing one
// microsecond past it.
func DurationMicros(samples int64, rate int) int64 {
	if rate <= 0 || samples <= 0 {
		return 0
	}
	return mulDivMicros(samples, int64(rate), true)
}

// SpanMicros converts a span to the microsecond interval reported for it, and
// enforces 0 <= start < end <= duration. A span whose end runs past the file
// (trailing pre-roll, or a flush that padded the tail) is clamped to the
// duration rather than rejected.
func SpanMicros(span Span, rate int, durationSamples int64) (startUS, endUS int64, err error) {
	switch {
	case rate <= 0:
		return 0, 0, fmt.Errorf("transcription: sample rate %d is not positive", rate)
	case durationSamples < 0:
		return 0, 0, fmt.Errorf("transcription: duration %d samples is negative", durationSamples)
	case durationSamples > maxTimelineSamples:
		return 0, 0, fmt.Errorf("transcription: duration %d samples cannot be represented in microseconds", durationSamples)
	case span.StartSample < 0:
		return 0, 0, fmt.Errorf("transcription: span starts at sample %d, before the file", span.StartSample)
	case span.EndSample <= span.StartSample:
		return 0, 0, fmt.Errorf("transcription: span [%d,%d) covers no audio", span.StartSample, span.EndSample)
	case span.StartSample >= durationSamples:
		return 0, 0, fmt.Errorf("transcription: span starts at sample %d, past the %d samples of audio", span.StartSample, durationSamples)
	}

	end := min(span.EndSample, durationSamples)
	startUS = SamplesToMicros(span.StartSample, rate)
	endUS = DurationMicros(end, rate)
	if endUS <= startUS {
		return 0, 0, fmt.Errorf("transcription: span [%d,%d) is shorter than one microsecond at %d Hz", span.StartSample, span.EndSample, rate)
	}
	return startUS, endUS, nil
}

// mulDivMicros computes samples * microsPerSecond / rate in 128-bit arithmetic
// so neither the floor multiply nor the ceiling (num+rate-1)/rate form can wrap
// an int64. A result that still cannot fit in int64 saturates at MaxInt64;
// SpanMicros rejects that case before converting.
func mulDivMicros(samples, rate int64, ceil bool) int64 {
	hi, lo := bits.Mul64(uint64(samples), uint64(microsPerSecond))
	denom := uint64(rate)
	if hi >= denom {
		return math.MaxInt64
	}
	q, rem := bits.Div64(hi, lo, denom)
	if ceil && rem != 0 {
		if q == math.MaxUint64 {
			return math.MaxInt64
		}
		q++
	}
	if q > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(q)
}
