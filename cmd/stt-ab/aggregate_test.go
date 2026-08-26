package main

import (
	"math"
	"testing"
)

const msToNS = 1_000_000

func TestParseResultsRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"not json", "this is not json"},
		{"empty string", ""},
		{"empty array", `[]`},
		// MarshalIndent writes a nil slice as null, so a run that produced
		// nothing arrives like this rather than as [].
		{"null", `null`},
		{"envelope with no results", `{"schema":"samantha.benchmark.v1","results":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseResults([]byte(tc.in)); err == nil {
				t.Fatalf("ParseResults(%q) succeeded, want error", tc.in)
			}
		})
	}
}

// scripts/bench.sh wraps the bare array in {schema, provenance, thresholds,
// results}; committed artifacts are envelopes while a fresh --json-file run is
// bare. Both must read.
func TestParseResultsAcceptsBothShapes(t *testing.T) {
	run := `{"mode":"stt","provider":"sherpa","metrics":{"STTFinalElapsed":1000000}}`
	tests := []struct {
		name string
		in   string
	}{
		{"bare array", "[" + run + "]"},
		{"bench.sh envelope", `{"schema":"samantha.benchmark.v1","provenance":{},"results":[` + run + `]}`},
		{"leading whitespace", "\n  [" + run + "]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := ParseResults([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseResults: %v", err)
			}
			if len(results) != 1 || results[0].Provider != "sherpa" {
				t.Fatalf("ParseResults = %+v, want one sherpa run", results)
			}
		})
	}
}

func TestDropWarmupRemovesOnlyIterationOne(t *testing.T) {
	results := []rawResult{
		{Iteration: 1, Fixture: "a.wav"},
		{Iteration: 2, Fixture: "a.wav"},
		{Iteration: 3, Fixture: "a.wav"},
		{Iteration: 1, Fixture: "b.wav"},
	}
	kept := DropWarmup(results)
	if len(kept) != 2 {
		t.Fatalf("DropWarmup kept %d runs, want 2", len(kept))
	}
	for _, r := range kept {
		if r.Iteration == 1 {
			t.Fatalf("DropWarmup kept iteration 1: %+v", r)
		}
	}
}

func TestParseResultsReadsDurationsAsNanoseconds(t *testing.T) {
	// events.TurnMetrics has no json tags, so the key is the Go field name
	// and the value is an int64 nanosecond count.
	raw := `[{"mode":"stt","provider":"sherpa","metrics":{"STTFinalElapsed":5108600000}}]`
	results, err := ParseResults([]byte(raw))
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if got, want := results[0].sttFinalMS(), 5108.6; math.Abs(got-want) > 0.001 {
		t.Fatalf("sttFinalMS = %v, want %v", got, want)
	}
}

func TestSTTRunsByProviderFiltersNonSTT(t *testing.T) {
	results := []rawResult{
		{Mode: "stt", Provider: "sherpa"},
		{Mode: "voice", Provider: "sherpa"},
		{Mode: "text", Provider: ""},
		{Mode: "stt", Provider: ""},
		{Mode: "stt", Provider: "parakeet"},
		{Mode: "stt", Provider: "sherpa"},
	}
	byProvider := STTRunsByProvider(results)
	if len(byProvider) != 2 {
		t.Fatalf("got %d providers, want 2: %v", len(byProvider), providerNames(byProvider))
	}
	if got := len(byProvider["sherpa"]); got != 2 {
		t.Fatalf("sherpa runs = %d, want 2", got)
	}
}

// The L2 case: a provider that emits trailing partials but never finalizes.
// Counting empty transcripts instead of missing finals would score these as
// successes, which is the exact mistake this metric exists to catch.
func TestStatsForCountsMissingFinalsNotEmptyTranscripts(t *testing.T) {
	runs := []rawResult{
		{Fixture: "a.wav", Expected: "hello there", Transcript: "hello there",
			Metrics: rawMetrics{STTFinalElapsed: 100 * msToNS}, TranscriptScore: 1.0},
		// Fragment: text present, no final. Must count as a missing final.
		{Fixture: "b.wav", Expected: "how are you", Transcript: "how are",
			Metrics: rawMetrics{STTFinalElapsed: 0}, TranscriptScore: 0.5},
		// Hard failure: no text, no final.
		{Fixture: "c.wav", Expected: "good morning", Transcript: "",
			Metrics: rawMetrics{STTFinalElapsed: 0}},
	}
	stats := StatsFor(runs)
	if stats.RunsWithoutAFinalTranscript != 2 {
		t.Fatalf("RunsWithoutAFinalTranscript = %d, want 2", stats.RunsWithoutAFinalTranscript)
	}
	if stats.Runs != 3 {
		t.Fatalf("Runs = %d, want 3", stats.Runs)
	}
	// Only the one real final may inform latency.
	if stats.MedianSTTFinalMS == nil || *stats.MedianSTTFinalMS != 100 {
		t.Fatalf("MedianSTTFinalMS = %v, want 100", stats.MedianSTTFinalMS)
	}
	// A zero score is absent from the JSON (omitempty) and reads back as 0;
	// it is a real data point whenever the run had expected text.
	if stats.MeanTranscriptScore == nil || math.Abs(*stats.MeanTranscriptScore-0.5) > 0.0001 {
		t.Fatalf("MeanTranscriptScore = %v, want 0.5", stats.MeanTranscriptScore)
	}
}

func TestStatsForAllRunsMissingFinalsYieldsNullLatency(t *testing.T) {
	runs := []rawResult{
		{Fixture: "a.wav", Metrics: rawMetrics{STTFinalElapsed: 0}},
		{Fixture: "b.wav", Metrics: rawMetrics{STTFinalElapsed: 0}},
	}
	stats := StatsFor(runs)
	if stats.MedianSTTFinalMS != nil || stats.P90STTFinalMS != nil {
		t.Fatalf("latency should be null with no finals, got median=%v p90=%v",
			stats.MedianSTTFinalMS, stats.P90STTFinalMS)
	}
	if stats.RunsWithoutAFinalTranscript != 2 {
		t.Fatalf("RunsWithoutAFinalTranscript = %d, want 2", stats.RunsWithoutAFinalTranscript)
	}
}

func TestStatsForCountsFixturesIterationsAndErrors(t *testing.T) {
	runs := []rawResult{
		{Fixture: "a.wav", Iteration: 1, Metrics: rawMetrics{STTFinalElapsed: 10 * msToNS}},
		{Fixture: "a.wav", Iteration: 2, Metrics: rawMetrics{STTFinalElapsed: 20 * msToNS}},
		{Fixture: "b.wav", Iteration: 1, Metrics: rawMetrics{STTFinalElapsed: 30 * msToNS},
			Errors: []rawError{{Stage: "benchmark", Message: "stt timed out"}}},
	}
	stats := StatsFor(runs)
	if stats.Fixtures != 2 {
		t.Fatalf("Fixtures = %d, want 2", stats.Fixtures)
	}
	if stats.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", stats.Iterations)
	}
	if stats.RunsWithErrors != 1 {
		t.Fatalf("RunsWithErrors = %d, want 1", stats.RunsWithErrors)
	}
}

func TestStatsForSampleTranscriptsDedupeAndCap(t *testing.T) {
	runs := []rawResult{
		{Transcript: "one"}, {Transcript: "one"}, {Transcript: "two"},
		{Transcript: "three"}, {Transcript: "four"}, {Transcript: "five"},
		{Transcript: ""},
	}
	stats := StatsFor(runs)
	if len(stats.SampleTranscripts) != maxSampleTranscripts {
		t.Fatalf("SampleTranscripts = %v, want %d entries", stats.SampleTranscripts, maxSampleTranscripts)
	}
	want := []string{"one", "two", "three", "four"}
	for i, w := range want {
		if stats.SampleTranscripts[i] != w {
			t.Fatalf("SampleTranscripts[%d] = %q, want %q", i, stats.SampleTranscripts[i], w)
		}
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		p      float64
		want   *float64
	}{
		{"empty is null", nil, 50, nil},
		{"single value", []float64{7}, 90, ptr(7)},
		{"median interpolates", []float64{1, 2, 3, 4}, 50, ptr(2.5)},
		{"p90 interpolates", []float64{1, 2, 3, 4}, 90, ptr(3.7)},
		{"unsorted input", []float64{4, 1, 3, 2}, 50, ptr(2.5)},
		{"p0 is min", []float64{5, 9, 1}, 0, ptr(1)},
		{"p100 is max", []float64{5, 9, 1}, 100, ptr(9)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := percentile(tc.values, tc.p)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("percentile = %v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("percentile = nil, want %v", *tc.want)
			case tc.want != nil && math.Abs(*got-*tc.want) > 0.0001:
				t.Fatalf("percentile = %v, want %v", *got, *tc.want)
			}
		})
	}
}

// Reproduces L4's published comparison so the arithmetic is pinned to a
// committed artifact rather than to itself.
func TestCompareMatchesL4PublishedNumbers(t *testing.T) {
	control := ProviderStats{
		MeanTranscriptScore: ptr(0.9116),
		MedianSTTFinalMS:    ptr(5108.6),
	}
	candidate := ProviderStats{
		MeanTranscriptScore: ptr(0.9246),
		MedianSTTFinalMS:    ptr(4580.1),
	}
	got := Compare(control, candidate)
	if got.AccuracyGapPoints == nil || math.Abs(*got.AccuracyGapPoints-1.3) > 0.0001 {
		t.Fatalf("AccuracyGapPoints = %v, want 1.3", got.AccuracyGapPoints)
	}
	if got.LatencyDeltaMS == nil || math.Abs(*got.LatencyDeltaMS-(-528.5)) > 0.0001 {
		t.Fatalf("LatencyDeltaMS = %v, want -528.5", got.LatencyDeltaMS)
	}
	if got.FinalsRegressed {
		t.Fatal("FinalsRegressed = true, want false")
	}
}

func TestCompareFlagsFinalsRegression(t *testing.T) {
	control := ProviderStats{RunsWithoutAFinalTranscript: 0}
	candidate := ProviderStats{RunsWithoutAFinalTranscript: 6}
	if !Compare(control, candidate).FinalsRegressed {
		t.Fatal("FinalsRegressed = false, want true when the candidate loses finals")
	}
}

func TestCompareLeavesNullsWhenAProviderHasNoFinals(t *testing.T) {
	control := ProviderStats{MeanTranscriptScore: ptr(0.9), MedianSTTFinalMS: ptr(100)}
	candidate := ProviderStats{MeanTranscriptScore: ptr(0.9)} // no finals at all
	got := Compare(control, candidate)
	if got.LatencyDeltaMS != nil {
		t.Fatalf("LatencyDeltaMS = %v, want nil", *got.LatencyDeltaMS)
	}
	if got.AccuracyGapPoints == nil {
		t.Fatal("AccuracyGapPoints = nil, want a value")
	}
}

func ptr(v float64) *float64 { return &v }
