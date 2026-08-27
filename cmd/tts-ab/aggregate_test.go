package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// turn builds a raw result the way `obey-voice benchmark` writes one, so the
// tests exercise the real wire shape (Go field names, int64 nanoseconds).
func turn(engine, tier string, iteration int, segs ...rawSegment) rawResult {
	var firstAudio int64
	if len(segs) > 0 {
		firstAudio = int64(time.Second) + segs[0].SynthElapsed
	}
	return rawResult{
		Mode:      "voice",
		Iteration: iteration,
		Fixture:   "f1.wav",
		Metrics: rawMetrics{
			FirstModelChunkElapsed: int64(500 * time.Millisecond),
			ModelCompleteElapsed:   int64(2 * time.Second),
			FirstSegmentElapsed:    int64(time.Second),
			FirstAudioReadyElapsed: firstAudio,
			PlaybackStartElapsed:   firstAudio,
			TTSProvider:            engine,
			TTSTier:                tier,
			Segments:               segs,
		},
	}
}

// seg builds a segment with no queue wait, so engine and total agree. The
// queued case is exercised separately.
func seg(index int, synth time.Duration, audioS float64) rawSegment {
	return rawSegment{Index: index, EngineElapsed: int64(synth), SynthElapsed: int64(synth), AudioSeconds: audioS}
}

func value(t *testing.T, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatal("expected a value, got nil")
	}
	return *p
}

func TestTurnsByEngineGroupsOnTheTierToo(t *testing.T) {
	// 0.6b and 1.7b are different engines. Pooling them would describe
	// neither: the same code measured 1.33x and 1.87x realtime.
	results := []rawResult{
		turn("qwen3-tts", "0.6b", 1, seg(0, time.Second, 1)),
		turn("qwen3-tts", "1.7b", 1, seg(0, 2*time.Second, 1)),
		turn("kokoro", "", 1, seg(0, time.Second, 1)),
	}
	byEngine := TurnsByEngine(results)
	for _, want := range []string{"qwen3-tts:0.6b", "qwen3-tts:1.7b", "kokoro"} {
		if len(byEngine[want]) != 1 {
			t.Errorf("byEngine[%q] = %d turns, want 1", want, len(byEngine[want]))
		}
	}
}

func TestTurnsByEngineSkipsTurnsWithoutAnEngine(t *testing.T) {
	// A run from a build that predates TTSProvider, or one made with
	// --no-voice, has nothing to attribute. Silently bucketing it under ""
	// would invent a column.
	results := []rawResult{turn("", "", 1, seg(0, time.Second, 1))}
	if got := TurnsByEngine(results); len(got) != 0 {
		t.Fatalf("TurnsByEngine() = %v, want empty", got)
	}
}

func TestStatsForRealtimeFactor(t *testing.T) {
	// 1.5s of synthesis for 1s of audio is 1.5x realtime.
	runs := []rawResult{
		turn("qwen3-tts", "0.6b", 2, seg(0, 1500*time.Millisecond, 1.0), seg(1, 3*time.Second, 2.0)),
	}
	stats := StatsFor(runs)

	if stats.Segments != 2 {
		t.Fatalf("Segments = %d, want 2", stats.Segments)
	}
	if got := value(t, stats.MedianRealtimeFactor); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("MedianRealtimeFactor = %v, want 1.5", got)
	}
	if got := value(t, stats.MedianFirstSegmentRTF); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("MedianFirstSegmentRTF = %v, want 1.5", got)
	}
	if got := value(t, stats.MedianSegmentsPerTurn); got != 2 {
		t.Errorf("MedianSegmentsPerTurn = %v, want 2", got)
	}
}

func TestStatsForExcludesSilentTurnsFromLatency(t *testing.T) {
	// A turn that never produced audio must not enter the latency
	// percentiles: including its zero would drag the median down and make an
	// engine that mostly failed read as the fast one.
	silent := turn("qwen3-tts", "1.7b", 2)
	silent.Metrics.PlaybackStartElapsed = 0
	silent.Metrics.FirstAudioReadyElapsed = 0

	audible := turn("qwen3-tts", "1.7b", 2, seg(0, 4*time.Second, 2.0))

	stats := StatsFor([]rawResult{silent, audible})
	if stats.TurnsWithoutAudio != 1 {
		t.Fatalf("TurnsWithoutAudio = %d, want 1", stats.TurnsWithoutAudio)
	}
	if got := value(t, stats.MedianSynthStageMS); math.Abs(got-4000) > 1 {
		t.Errorf("MedianSynthStageMS = %v, want the audible turn's 4000", got)
	}
}

func TestStatsForKeepsModelCompleteToAudioSigned(t *testing.T) {
	// Negative means audio started before the reply finished — the
	// progressive path working. Clamping it would erase the one distinction
	// this figure exists to make.
	r := turn("kokoro", "", 2, seg(0, 500*time.Millisecond, 1.0))
	r.Metrics.ModelCompleteElapsed = int64(5 * time.Second)
	r.Metrics.PlaybackStartElapsed = int64(1500 * time.Millisecond)

	stats := StatsFor([]rawResult{r})
	if got := value(t, stats.MedianModelCompleteToAudioMS); got != -3500 {
		t.Errorf("MedianModelCompleteToAudioMS = %v, want -3500", got)
	}
}

// TestStatsForExcludesFallbackTurnsFromLatency is the review finding on #31.
// TurnMetrics.TTSProvider names the CONFIGURED engine, stamped before
// synthesis, so a turn that fell back to Kokoro still carries qwen3-tts's
// name. Aggregating it credited the slow engine with the fast one's timings —
// and Kokoro is roughly 4x faster here, so a few contaminated turns could
// reverse the decision the artifact exists to record.
func TestStatsForExcludesFallbackTurnsFromLatency(t *testing.T) {
	// A clean qwen turn: 2.4x realtime.
	clean := turn("qwen3-tts", "1.7b", 2, seg(0, 2400*time.Millisecond, 1.0))
	// A turn that fell back: Kokoro's 0.64x, still labelled qwen3-tts.
	contaminated := turn("qwen3-tts", "1.7b", 2, rawSegment{
		Index: 0, EngineElapsed: int64(640 * time.Millisecond),
		SynthElapsed: int64(640 * time.Millisecond), AudioSeconds: 1.0, Fallback: true,
	})
	contaminated.Metrics.TTSFallbacks = 1

	stats := StatsFor([]rawResult{clean, contaminated})

	if stats.TurnsWithFallback != 1 {
		t.Fatalf("TurnsWithFallback = %d, want 1", stats.TurnsWithFallback)
	}
	if stats.SegmentsFallback != 1 {
		t.Errorf("SegmentsFallback = %d, want 1", stats.SegmentsFallback)
	}
	if stats.Segments != 1 {
		t.Errorf("Segments = %d, want only the clean turn's segment", stats.Segments)
	}
	// The whole point: the median must be the clean 2.4, not a blend pulled
	// toward Kokoro's 0.64.
	if got := value(t, stats.MedianRealtimeFactor); math.Abs(got-2.4) > 1e-9 {
		t.Errorf("MedianRealtimeFactor = %v, want 2.4 — the fallback turn must not count", got)
	}
}

// TestStatsForCatchesAFallbackSegmentWithoutTheTurnCounter guards the second
// route to the same contamination: the per-segment flag alone must be enough,
// because a reader should not have to trust two fields to agree.
func TestStatsForCatchesAFallbackSegmentWithoutTheTurnCounter(t *testing.T) {
	r := turn("qwen3-tts", "1.7b", 2, rawSegment{
		Index: 0, EngineElapsed: int64(640 * time.Millisecond),
		SynthElapsed: int64(640 * time.Millisecond), AudioSeconds: 1.0, Fallback: true,
	})
	// TTSFallbacks deliberately left at 0.
	stats := StatsFor([]rawResult{r})
	if stats.TurnsWithFallback != 1 {
		t.Errorf("TurnsWithFallback = %d, want 1 from the segment flag alone", stats.TurnsWithFallback)
	}
	if stats.MedianRealtimeFactor != nil {
		t.Errorf("MedianRealtimeFactor = %v, want nil: every segment fell back", *stats.MedianRealtimeFactor)
	}
}

func TestStatsForCountsFallbackAndWatchdogTurns(t *testing.T) {
	fell := turn("qwen3-tts", "1.7b", 2, seg(0, time.Second, 1))
	fell.Metrics.TTSFallbacks = 1
	fell.Metrics.Segments[0].Fallback = true

	stalled := turn("qwen3-tts", "1.7b", 2, seg(0, time.Second, 1))
	stalled.Metrics.StallWatchdogFired = true
	stalled.Metrics.FirstAudioGrace = int64(47 * time.Second)

	clean := turn("qwen3-tts", "1.7b", 2, seg(0, time.Second, 1))
	clean.Metrics.FirstAudioGrace = int64(47 * time.Second)

	stats := StatsFor([]rawResult{fell, stalled, clean})
	if stats.TurnsWithFallback != 1 {
		t.Errorf("TurnsWithFallback = %d, want 1", stats.TurnsWithFallback)
	}
	// The fallback turn is counted but not measured, so only the other two
	// contribute segments.
	if stats.Segments != 2 {
		t.Errorf("Segments = %d, want 2 (the fallback turn is excluded)", stats.Segments)
	}
	if stats.TurnsStallWatchdogFired != 1 {
		t.Errorf("TurnsStallWatchdogFired = %d, want 1", stats.TurnsStallWatchdogFired)
	}
	if got := value(t, stats.FirstAudioGraceMS); got != 47000 {
		t.Errorf("FirstAudioGraceMS = %v, want 47000", got)
	}
}

func TestStatsForDropsGraceWhenTheSweepMixedConfigurations(t *testing.T) {
	a := turn("qwen3-tts", "1.7b", 2, seg(0, time.Second, 1))
	a.Metrics.FirstAudioGrace = int64(20 * time.Second)
	b := turn("qwen3-tts", "1.7b", 2, seg(0, time.Second, 1))
	b.Metrics.FirstAudioGrace = int64(47 * time.Second)

	if got := StatsFor([]rawResult{a, b}).FirstAudioGraceMS; got != nil {
		t.Errorf("FirstAudioGraceMS = %v, want nil when turns disagree", *got)
	}
}

func TestCompareFlagsAudioRegressionAndBatchingThreshold(t *testing.T) {
	control := StatsFor([]rawResult{turn("kokoro", "", 2, seg(0, 1320*time.Millisecond, 1.0))})

	slow := turn("qwen3-tts", "1.7b", 2, seg(0, 1870*time.Millisecond, 1.0))
	silent := turn("qwen3-tts", "1.7b", 2)
	silent.Metrics.PlaybackStartElapsed = 0
	candidate := StatsFor([]rawResult{slow, silent})

	res := Compare(control, candidate)
	if !res.AudioRegressed {
		t.Error("AudioRegressed = false, want true when the candidate lost audio on more turns")
	}
	if res.ClearsBatchingThreshold == nil || *res.ClearsBatchingThreshold {
		t.Error("ClearsBatchingThreshold: 1.87x realtime must not clear the ~2.3x bar")
	}
	if got := value(t, res.RealtimeFactorDelta); math.Abs(got-0.55) > 1e-6 {
		t.Errorf("RealtimeFactorDelta = %v, want 0.55", got)
	}
}

func TestCompareClearsBatchingThresholdWhenFastEnough(t *testing.T) {
	// 2.5x realtime is an RTF of 0.4, which beats the 1/2.3 bar D009 set.
	control := StatsFor([]rawResult{turn("kokoro", "", 2, seg(0, 1320*time.Millisecond, 1.0))})
	candidate := StatsFor([]rawResult{turn("fast-tts", "", 2, seg(0, 400*time.Millisecond, 1.0))})

	res := Compare(control, candidate)
	if res.ClearsBatchingThreshold == nil || !*res.ClearsBatchingThreshold {
		t.Error("ClearsBatchingThreshold = false, want true at 2.5x realtime")
	}
}

// TestStatsForChargesTheEngineOnlyForSynthesis is the correction a real run
// forced: SynthElapsed on any segment after the first includes waiting behind
// the previous sentence's playback. Using it as the realtime factor made a
// 2.5x engine read as 10x, which would have shipped a wrong artifact.
func TestStatsForChargesTheEngineOnlyForSynthesis(t *testing.T) {
	queued := rawSegment{
		Index:         1,
		EngineElapsed: int64(2 * time.Second),
		SynthElapsed:  int64(10 * time.Second), // 8s of it waiting to be played
		AudioSeconds:  1.0,
	}
	stats := StatsFor([]rawResult{turn("qwen3-tts", "0.6b", 2, queued)})

	if got := value(t, stats.MedianRealtimeFactor); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("MedianRealtimeFactor = %v, want 2.0 from engine time alone", got)
	}
	if got := value(t, stats.MedianSegmentQueueMS); math.Abs(got-8000) > 1 {
		t.Errorf("MedianSegmentQueueMS = %v, want the 8000ms wait reported separately", got)
	}
	if got := value(t, stats.MedianSegmentEngineMS); math.Abs(got-2000) > 1 {
		t.Errorf("MedianSegmentEngineMS = %v, want 2000", got)
	}
}

// TestStatsForReadsPreSplitRecords keeps older raw runs aggregatable: they
// carry only the total, and refusing to score them would strand the artifacts
// already on disk.
func TestStatsForReadsPreSplitRecords(t *testing.T) {
	old := rawSegment{Index: 0, SynthElapsed: int64(2 * time.Second), AudioSeconds: 1.0}
	stats := StatsFor([]rawResult{turn("kokoro", "", 2, old)})
	if got := value(t, stats.MedianRealtimeFactor); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("MedianRealtimeFactor = %v, want 2.0 from the total when no engine time was recorded", got)
	}
}

func TestDropWarmupRemovesIterationOne(t *testing.T) {
	results := []rawResult{
		turn("qwen3-tts", "0.6b", 1, seg(0, 8*time.Second, 1)),
		turn("qwen3-tts", "0.6b", 2, seg(0, time.Second, 1)),
	}
	kept := DropWarmup(results)
	if len(kept) != 1 || kept[0].Iteration != 2 {
		t.Fatalf("DropWarmup() = %+v, want only iteration 2", kept)
	}
}

func TestParseResultsAcceptsBareArrayAndEnvelope(t *testing.T) {
	array, err := json.Marshal([]rawResult{turn("kokoro", "", 1, seg(0, time.Second, 1))})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseResults(array); err != nil || len(got) != 1 {
		t.Fatalf("ParseResults(array) = %v, %v", got, err)
	}

	envelope, err := json.Marshal(benchmarkEnvelope{Results: []rawResult{turn("kokoro", "", 1, seg(0, time.Second, 1))}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseResults(envelope); err != nil || len(got) != 1 {
		t.Fatalf("ParseResults(envelope) = %v, %v", got, err)
	}

	if _, err := ParseResults([]byte("  ")); err == nil {
		t.Error("ParseResults(empty) = nil error, want a failure")
	}
	if _, err := ParseResults([]byte("null")); err == nil {
		t.Error("ParseResults(null) = nil error, want a failure")
	}
}
