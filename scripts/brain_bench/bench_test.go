package main

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// pct and evaluate are the two pure functions the verdict rests on: pct
// computes the gated statistic and evaluate applies the thresholds. Neither
// needs a machine, which is the same reason cmd/tts-ab keeps its aggregator
// pure and unit-tests it.

func TestPctNearestRank(t *testing.T) {
	// 1..10 so the expected rank is readable: nearest-rank puts p50 at
	// ceil(0.50*10)=5 -> 5, p95 at ceil(0.95*10)=10 -> 10.
	ten := []float64{10, 1, 9, 2, 8, 3, 7, 4, 6, 5}

	tests := []struct {
		name string
		in   []float64
		p    int
		want float64
	}{
		{"p50 of 1..10 is rank 5", ten, 50, 5},
		{"p95 of 1..10 is rank 10", ten, 95, 10},
		{"p100 is the max", ten, 100, 10},
		{"input order does not matter", []float64{3, 1, 2}, 50, 2},
		{"single sample is every percentile", []float64{42}, 95, 42},
		{"empty is zero, not a panic", nil, 50, 0},
		{"p0 clamps to rank 1", ten, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pct(tc.in, tc.p); got != tc.want {
				t.Errorf("pct(%v, %d) = %v, want %v", tc.in, tc.p, got, tc.want)
			}
		})
	}
}

// TestPctP95EqualsMaxAtFifteenSamples pins the caveat the results doc makes in
// prose: at the default -reps 3 over 5 prompts, n=15 and nearest-rank p95
// resolves to rank 15 — the maximum. The column is therefore "worst observed",
// not a distributional claim.
//
// This is a test so that raising -reps silently changes what the p95 column
// means and something notices. At n=20 the ranks separate.
func TestPctP95EqualsMaxAtFifteenSamples(t *testing.T) {
	fifteen := make([]float64, 15)
	for i := range fifteen {
		fifteen[i] = float64(i + 1)
	}
	if p95, max := pct(fifteen, 95), pct(fifteen, 100); p95 != max {
		t.Errorf("at n=15 p95 (%v) must equal max (%v) — the doc's caveat depends on it", p95, max)
	}

	twenty := make([]float64, 20)
	for i := range twenty {
		twenty[i] = float64(i + 1)
	}
	if p95, max := pct(twenty, 95), pct(twenty, 100); p95 == max {
		t.Errorf("at n=20 p95 (%v) should separate from max (%v); if it no longer does, the doc's n=15 caveat needs rewording", p95, max)
	}
}

func TestEvaluateAppliesTheCriteriaThresholds(t *testing.T) {
	// The gate from docs/benchmarks/B1-brain-latency-ab-criteria.md:
	// p50 <= 2000ms, p95 <= 4000ms, gen >= 25 tok/s, worst turn <= 90000ms.
	pass := summary{TTFTp50MS: 2000, TTFTp95MS: 4000, GenTokSMedian: 25, TotalMaxMS: 90000}

	tests := []struct {
		name string
		in   summary
		want gateResult
	}{
		{"every threshold exactly on the boundary passes", pass, gateResult{true, true, true, true}},
		{"p50 one ms over fails G1", over(pass, func(s *summary) { s.TTFTp50MS = 2000.001 }), gateResult{false, true, true, true}},
		{"p95 one ms over fails G2", over(pass, func(s *summary) { s.TTFTp95MS = 4000.001 }), gateResult{true, false, true, true}},
		{"a hair under 25 tok/s fails G3", over(pass, func(s *summary) { s.GenTokSMedian = 24.999 }), gateResult{true, true, false, true}},
		{"worst turn over 90s fails G4", over(pass, func(s *summary) { s.TotalMaxMS = 90000.001 }), gateResult{true, true, true, false}},
		{
			"the incumbent's measured empty-history numbers fail G1, G2 and G3",
			summary{TTFTp50MS: 4636.9, TTFTp95MS: 4824.2, GenTokSMedian: 23.9, TotalMaxMS: 8780},
			gateResult{false, false, false, true},
		},
		{
			"gemma4's measured 6k numbers pass everything",
			summary{TTFTp50MS: 388, TTFTp95MS: 423, GenTokSMedian: 92.5, TotalMaxMS: 1300},
			gateResult{true, true, true, true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluate(tc.in)
			if got != tc.want {
				t.Errorf("evaluate(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
			if got.pass() != (tc.want == gateResult{true, true, true, true}) {
				t.Errorf("pass() disagrees with the individual criteria: %+v", got)
			}
		})
	}
}

func over(base summary, mutate func(*summary)) summary {
	s := base
	mutate(&s)
	return s
}

// sizeByChars stands in for the brain's estimator: it only has to be monotonic
// in history length for the windowing to be exercised.
func sizeByChars(turns []brain.Turn) int {
	n := 0
	for _, t := range turns {
		n += len(t.Content)
	}
	return n / 4
}

func TestBuildFilledHistoryTakesAUserAnchoredTail(t *testing.T) {
	full := sizeByChars(filledHistoryScript)

	got, err := buildFilledHistory(full, sizeByChars)
	if err != nil {
		t.Fatalf("a target equal to the whole script should fit: %v", err)
	}
	if len(got) != len(filledHistoryScript) {
		t.Errorf("got %d turns, want the whole script (%d)", len(got), len(filledHistoryScript))
	}

	// Half the budget must yield a strict, shorter suffix that still opens on a
	// user turn — a window starting mid-exchange would show the model an
	// assistant reply with no question in front of it.
	half, err := buildFilledHistory(full/2, sizeByChars)
	if err != nil {
		t.Fatalf("half the script should fit: %v", err)
	}
	if len(half) == 0 || len(half) >= len(filledHistoryScript) {
		t.Fatalf("got %d turns, want a strict suffix of %d", len(half), len(filledHistoryScript))
	}
	if half[0].Role != "user" {
		t.Errorf("window opens on %q, want it to open on a user turn", half[0].Role)
	}
	if sizeByChars(half) > full/2 {
		t.Errorf("window is %d tokens, over the %d budget", sizeByChars(half), full/2)
	}
	// It is a tail, not an arbitrary slice.
	tail := filledHistoryScript[len(filledHistoryScript)-len(half):]
	if half[0].Content != tail[0].Content {
		t.Error("window is not the tail of the script")
	}
}

// TestBuildFilledHistoryRejectsAnImpossibleTarget guards the case that would
// otherwise be invisible: with a target below even the shortest window, an
// unguarded implementation returns nil, the filled condition silently becomes a
// second copy of the empty one, and the artifact reports turns: 0 having
// measured the same thing twice.
func TestBuildFilledHistoryRejectsAnImpossibleTarget(t *testing.T) {
	got, err := buildFilledHistory(1, sizeByChars)
	if err == nil {
		t.Fatalf("want an error for an unreachable target, got %d turns", len(got))
	}
	if got != nil {
		t.Errorf("want a nil history alongside the error, got %d turns", len(got))
	}
	if !strings.Contains(err.Error(), "history-tokens") {
		t.Errorf("error should name the flag the operator has to change, got: %v", err)
	}
}

// The script itself is a fixture the measurement depends on: an assistant turn
// at the front would make every tail window that starts there incoherent.
func TestFilledHistoryScriptAlternatesFromAUserTurn(t *testing.T) {
	if len(filledHistoryScript) == 0 {
		t.Fatal("the filled-history script is empty")
	}
	if filledHistoryScript[0].Role != "user" {
		t.Errorf("script opens on %q, want user", filledHistoryScript[0].Role)
	}
	for i, turn := range filledHistoryScript {
		want := "user"
		if i%2 == 1 {
			want = "assistant"
		}
		if turn.Role != want {
			t.Fatalf("turn %d is %q, want %q — the script must alternate", i, turn.Role, want)
		}
		if strings.TrimSpace(turn.Content) == "" {
			t.Errorf("turn %d is empty", i)
		}
	}
}

// warmupPrompt must never collide with a measured prompt. If it does, that
// prompt's first rep lands on a total KV-cache hit (306 ms against 4.5 s when
// this happened for real) and the gated median moves.
func TestWarmupPromptIsNotAMeasuredPrompt(t *testing.T) {
	for _, p := range benchPrompts {
		if p.ID == warmupPrompt.ID || p.Text == warmupPrompt.Text {
			t.Errorf("warm-up collides with measured prompt %s — its first rep will measure the cache, not the model", p.ID)
		}
	}
}
