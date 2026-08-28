package scene

import (
	"errors"
	"testing"
	"time"
)

func pcm(rate, samples int) TurnPCM {
	return TurnPCM{Samples: make([]float32, samples), Rate: rate}
}

func TestSceneConcatTurnsRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		turns    []TurnPCM
		pause    time.Duration
		resample Resampler
	}{
		"no resampler": {[]TurnPCM{pcm(24000, 10)}, 0, nil},
		"negative pause": {
			[]TurnPCM{pcm(24000, 10)}, -time.Millisecond, ResampleLinear,
		},
		"no turns":         {nil, 0, ResampleLinear},
		"all turns failed": {[]TurnPCM{}, 400 * time.Millisecond, ResampleLinear},
		"no turn produced audio": {
			[]TurnPCM{{Rate: 24000}, {Rate: 24000}}, 400 * time.Millisecond, ResampleLinear,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ConcatTurns(tc.turns, tc.pause, tc.resample); err == nil {
				t.Fatal("ConcatTurns accepted input it cannot mix")
			}
		})
	}
}

// A resampler failure has to name the turn it happened on, or a mixed-rate cast
// reports a length error with no way to find the offending line.
func TestSceneConcatTurnsReportsResampleFailure(t *testing.T) {
	boom := errors.New("resampler exploded")
	_, err := ConcatTurns([]TurnPCM{pcm(24000, 10), pcm(16000, 10)}, 0,
		func([]float32, int, int) ([]float32, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the resampler's own error", err)
	}
}

// The pause is exactly pause*rate zero samples between turns, with none before
// the first or after the last.
func TestSceneConcatTurnsPauseSampleCount(t *testing.T) {
	const rate = 24000
	turns := []TurnPCM{pcm(rate, 2400), pcm(rate, 2400), pcm(rate, 2400)}
	combined, err := ConcatTurns(turns, 400*time.Millisecond, ResampleLinear)
	if err != nil {
		t.Fatalf("ConcatTurns: %v", err)
	}
	wantPause := int(0.4 * rate)
	want := 3*2400 + 2*wantPause
	if len(combined.Samples) != want {
		t.Fatalf("combined %d samples, want %d (3 turns + 2 pauses)", len(combined.Samples), want)
	}
	if combined.PauseMS != 400 {
		t.Errorf("pause_ms = %d, want 400", combined.PauseMS)
	}
	if combined.Rate != rate {
		t.Errorf("rate = %d, want %d", combined.Rate, rate)
	}
}

// The clock identity the video step relies on: every turn starts exactly one
// duration plus one pause after the previous one, in whole milliseconds.
func TestSceneConcatTurnsStartIdentity(t *testing.T) {
	const rate = 24000
	turns := []TurnPCM{pcm(rate, 4001), pcm(rate, 7333), pcm(rate, 991), pcm(rate, 24000)}
	combined, err := ConcatTurns(turns, 250*time.Millisecond, ResampleLinear)
	if err != nil {
		t.Fatalf("ConcatTurns: %v", err)
	}
	if combined.Clocks[0].StartMS != 0 {
		t.Fatalf("first turn starts at %d ms, want 0 (no leading silence)", combined.Clocks[0].StartMS)
	}
	for i := 0; i+1 < len(combined.Clocks); i++ {
		want := combined.Clocks[i].StartMS + combined.Clocks[i].DurationMS + combined.PauseMS
		if combined.Clocks[i+1].StartMS != want {
			t.Fatalf("turn %d starts at %d ms, want %d", i+1, combined.Clocks[i+1].StartMS, want)
		}
	}
	last := combined.Clocks[len(combined.Clocks)-1]
	if combined.DurationMS != last.StartMS+last.DurationMS {
		t.Errorf("duration_ms = %d, want %d (no trailing silence)", combined.DurationMS, last.StartMS+last.DurationMS)
	}
}

// Two mouths at different rates mix at the first turn's rate. v1 personas are
// usually both 24 kHz, but the rates are runtime facts and providers refuse to
// convert, so the conversion has to happen here.
func TestSceneConcatTurnsMixedRates(t *testing.T) {
	const rate = 24000
	turns := []TurnPCM{pcm(rate, 2400), pcm(16000, 1600)}
	combined, err := ConcatTurns(turns, 0, ResampleLinear)
	if err != nil {
		t.Fatalf("ConcatTurns: %v", err)
	}
	if combined.Rate != rate {
		t.Fatalf("rate = %d, want the first turn's %d", combined.Rate, rate)
	}
	// 1600 samples of 16 kHz audio is 100ms, which is 2400 samples at 24 kHz.
	if len(combined.Samples) != 4800 {
		t.Fatalf("combined %d samples, want 4800", len(combined.Samples))
	}
	if combined.Clocks[1].DurationMS != 100 {
		t.Errorf("resampled turn duration = %d ms, want 100", combined.Clocks[1].DurationMS)
	}
}

// A line that synthesized to silence takes no time and earns no pause: it is a
// recorded turn with nothing in it, not a gap in the conversation.
func TestSceneConcatTurnsSkipsSilentTurns(t *testing.T) {
	const rate = 24000
	turns := []TurnPCM{pcm(rate, 2400), {Rate: rate}, pcm(rate, 2400)}
	combined, err := ConcatTurns(turns, 400*time.Millisecond, ResampleLinear)
	if err != nil {
		t.Fatalf("ConcatTurns: %v", err)
	}
	wantPause := int(0.4 * rate)
	if want := 2*2400 + wantPause; len(combined.Samples) != want {
		t.Fatalf("combined %d samples, want %d (one pause, not two)", len(combined.Samples), want)
	}
	if combined.Clocks[1].DurationMS != 0 {
		t.Errorf("silent turn duration = %d ms, want 0", combined.Clocks[1].DurationMS)
	}
	if combined.Clocks[1].StartMS != 100 {
		t.Errorf("silent turn starts at %d ms, want 100 (the end of turn 1)", combined.Clocks[1].StartMS)
	}
}
