package transcription

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
)

const rate = 16000

func TestSamplesToMicrosRoundsDown(t *testing.T) {
	cases := []struct {
		samples int64
		want    int64
	}{
		{0, 0},
		{1, 62},  // 62.5 us, floored
		{2, 125}, // exact
		{16000, 1_000_000},
		{16000*90 + 216, 90_013_500}, // the 90 s meeting fixture
		{-5, 0},
	}
	for _, tc := range cases {
		if got := SamplesToMicros(tc.samples, rate); got != tc.want {
			t.Errorf("SamplesToMicros(%d) = %d, want %d", tc.samples, got, tc.want)
		}
	}
	if got := SamplesToMicros(16000, 0); got != 0 {
		t.Errorf("SamplesToMicros with no rate = %d, want 0", got)
	}
}

func TestDurationMicrosRoundsUp(t *testing.T) {
	cases := []struct {
		samples int64
		want    int64
	}{
		{0, 0},
		{1, 63}, // 62.5 us, rounded up
		{2, 125},
		{16000, 1_000_000},
	}
	for _, tc := range cases {
		if got := DurationMicros(tc.samples, rate); got != tc.want {
			t.Errorf("DurationMicros(%d) = %d, want %d", tc.samples, got, tc.want)
		}
	}
}

func TestSpanMicros(t *testing.T) {
	const duration = int64(16000 * 10) // 10 s

	cases := []struct {
		name      string
		span      Span
		rate      int
		duration  int64
		wantStart int64
		wantEnd   int64
		wantErr   string
	}{
		{
			name:      "whole seconds",
			span:      Span{StartSample: 16000, EndSample: 16000 * 3},
			rate:      rate,
			duration:  duration,
			wantStart: 1_000_000,
			wantEnd:   3_000_000,
		},
		{
			name:      "one sample in, one sample long",
			span:      Span{StartSample: 1, EndSample: 2},
			rate:      rate,
			duration:  duration,
			wantStart: 62,
			wantEnd:   125,
		},
		{
			name:      "span ending at EOF ends at the reported duration",
			span:      Span{StartSample: 16000 * 9, EndSample: duration},
			rate:      rate,
			duration:  duration,
			wantStart: 9_000_000,
			wantEnd:   DurationMicros(duration, rate),
		},
		{
			name:      "trailing padding is clamped to the duration",
			span:      Span{StartSample: 16000 * 9, EndSample: duration + 4800},
			rate:      rate,
			duration:  duration,
			wantStart: 9_000_000,
			wantEnd:   10_000_000,
		},
		{
			name:     "zero length span",
			span:     Span{StartSample: 16000, EndSample: 16000},
			rate:     rate,
			duration: duration,
			wantErr:  "span [16000,16000) covers no audio",
		},
		{
			name:     "end before start",
			span:     Span{StartSample: 16000, EndSample: 800},
			rate:     rate,
			duration: duration,
			wantErr:  "span [16000,800) covers no audio",
		},
		{
			name:     "negative start",
			span:     Span{StartSample: -1, EndSample: 800},
			rate:     rate,
			duration: duration,
			wantErr:  "starts at sample -1, before the file",
		},
		{
			name:     "start at the duration",
			span:     Span{StartSample: duration, EndSample: duration + 1600},
			rate:     rate,
			duration: duration,
			wantErr:  "starts at sample 160000, past the 160000 samples of audio",
		},
		{
			name:     "start past the duration",
			span:     Span{StartSample: duration + 1, EndSample: duration + 1600},
			rate:     rate,
			duration: duration,
			wantErr:  "starts at sample 160001, past the 160000 samples of audio",
		},
		{
			name:     "non-positive rate",
			span:     Span{StartSample: 0, EndSample: 1600},
			rate:     0,
			duration: duration,
			wantErr:  "sample rate 0 is not positive",
		},
		{
			name:     "negative rate",
			span:     Span{StartSample: 0, EndSample: 1600},
			rate:     -3,
			duration: duration,
			wantErr:  "sample rate -3 is not positive",
		},
		{
			name:     "negative duration",
			span:     Span{StartSample: 0, EndSample: 1600},
			rate:     rate,
			duration: -7,
			wantErr:  "duration -7 samples is negative",
		},
		{
			name:     "duration that cannot be represented",
			span:     Span{StartSample: 0, EndSample: 1600},
			rate:     rate,
			duration: maxTimelineSamples + 1,
			wantErr:  fmt.Sprintf("duration %d samples cannot be represented in microseconds", maxTimelineSamples+1),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := SpanMicros(tc.span, tc.rate, tc.duration)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("SpanMicros = (%d, %d), want an error", start, end)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SpanMicros: %v", err)
			}
			if start != tc.wantStart || end != tc.wantEnd {
				t.Fatalf("SpanMicros = (%d, %d), want (%d, %d)", start, end, tc.wantStart, tc.wantEnd)
			}
			if !(0 <= start && start < end && end <= DurationMicros(tc.duration, tc.rate)) {
				t.Fatalf("invariant 0 <= %d < %d <= %d does not hold", start, end, DurationMicros(tc.duration, tc.rate))
			}
		})
	}
}

func TestSpanMicrosRejectsABadRate(t *testing.T) {
	_, _, err := SpanMicros(Span{StartSample: 0, EndSample: 1600}, 0, 16000)
	if err == nil || !strings.Contains(err.Error(), "sample rate 0") {
		t.Fatalf("error = %v, want a rate rejection", err)
	}
}

func TestTimingMathDoesNotOverflowTheAcceptedDomain(t *testing.T) {
	// samples * 1_000_000 sits 775807 below MaxInt64 at maxTimelineSamples, so
	// the naive ceiling (samples*1e6 + rate - 1) / rate wraps for any rate
	// above that slack. 1 MHz is well past it and still a legal SpanMicros rate.
	const overflowRate = 1_000_000

	if got, want := SamplesToMicros(maxTimelineSamples, overflowRate), int64(maxTimelineSamples); got != want {
		t.Fatalf("SamplesToMicros(%d, %d) = %d, want %d", maxTimelineSamples, overflowRate, got, want)
	}
	if got, want := DurationMicros(maxTimelineSamples, overflowRate), int64(maxTimelineSamples); got != want {
		t.Fatalf("DurationMicros(%d, %d) = %d, want %d", maxTimelineSamples, overflowRate, got, want)
	}

	ceilRate := overflowRate + 1
	got := DurationMicros(maxTimelineSamples, ceilRate)
	want := ceilMicrosOracle(t, maxTimelineSamples, ceilRate)
	if got != want {
		t.Fatalf("DurationMicros(%d, %d) = %d, want %d", maxTimelineSamples, ceilRate, got, want)
	}
	if got <= 0 {
		t.Fatalf("DurationMicros wrapped to %d", got)
	}

	start, end, err := SpanMicros(Span{StartSample: 0, EndSample: 1}, overflowRate, maxTimelineSamples)
	if err != nil {
		t.Fatalf("SpanMicros at maxTimelineSamples: %v", err)
	}
	if !(0 <= start && start < end && end <= DurationMicros(maxTimelineSamples, overflowRate)) {
		t.Fatalf("invariant 0 <= %d < %d <= %d does not hold", start, end, DurationMicros(maxTimelineSamples, overflowRate))
	}
}

func TestUnrepresentableConversionSaturates(t *testing.T) {
	if got := SamplesToMicros(math.MaxInt64, 1); got != math.MaxInt64 {
		t.Fatalf("SamplesToMicros(MaxInt64, 1) = %d, want MaxInt64 (no wrap)", got)
	}
	if got := DurationMicros(math.MaxInt64, 1); got != math.MaxInt64 {
		t.Fatalf("DurationMicros(MaxInt64, 1) = %d, want MaxInt64 (no wrap)", got)
	}
}

func ceilMicrosOracle(t *testing.T, samples int64, hz int) int64 {
	t.Helper()
	n := new(big.Int).Mul(big.NewInt(samples), big.NewInt(microsPerSecond))
	q, rem := new(big.Int).QuoRem(n, big.NewInt(int64(hz)), new(big.Int))
	if rem.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		t.Fatalf("oracle result %s does not fit in int64", q)
	}
	return q.Int64()
}
