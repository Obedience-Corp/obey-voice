package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func synthSine(sampleRate, n int, freq, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
	}
	return out
}

func TestScoreCleanSineHasNoCrackle(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 24_000, 440, 0.5)
	a := scoreSamples("sine.wav", samples, 24_000)
	if a.HasCrackle {
		t.Fatalf("clean sine reported crackle: %+v", a)
	}
	if a.SampleRate != 24_000 {
		t.Fatalf("sample_rate = %d, want 24000", a.SampleRate)
	}
	if a.DurationS < 0.99 || a.DurationS > 1.01 {
		t.Fatalf("duration_s = %v, want ~1", a.DurationS)
	}
	if a.RMS < 0.2 || a.Peak < 0.4 {
		t.Fatalf("rms=%v peak=%v, want audible sine", a.RMS, a.Peak)
	}
}

func TestScoreSingleClickAllowedUnderBudget(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 4_000, 440, 0.4)
	samples[2000] = 0.9 // a click, not a clipped peak
	samples[2001] = -0.9
	a := scoreSamples("click.wav", samples, 24_000)
	if a.HasCrackle {
		t.Fatalf("one click should sit under MaxImpulses=8: %+v", a)
	}
	if a.ImpulseCount < 1 {
		t.Fatalf("ImpulseCount = %d, want ≥ 1", a.ImpulseCount)
	}
}

func TestScoreManyClicksFails(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 8_000, 440, 0.4)
	for i := 0; i < 12; i++ {
		off := 400 + i*500
		samples[off] = 0.9
		samples[off+1] = -0.9
	}
	a := scoreSamples("clicks.wav", samples, 24_000)
	if !a.HasCrackle {
		t.Fatalf("12 clicks should exceed MaxImpulses=8: %+v", a)
	}
}

func TestScoreNaturalPausesAreReportedNotGated(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 8_000, 440, 0.5)
	// Three short interior near-silences, the shape of a vocoder's pauses.
	for _, start := range []int{1500, 3000, 4500} {
		for i := start; i < start+64; i++ {
			samples[i] = 0
		}
	}
	a := scoreSamples("pauses.wav", samples, 24_000)
	if a.HasCrackle {
		t.Fatalf("short pauses must not fail the gate: %+v", a)
	}
	if a.MidSilenceRuns < 3 || a.ZeroHoleRuns != 3 {
		t.Fatalf("pauses must still be reported: mid=%d zero=%d", a.MidSilenceRuns, a.ZeroHoleRuns)
	}
}

func TestScoreLongZeroHoleFails(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 24_000, 440, 0.5)
	for i := 8_000; i < 8_000+7_200; i++ { // 300 ms of exact zero inside speech
		samples[i] = 0
	}
	a := scoreSamples("stall.wav", samples, 24_000)
	if !a.HasCrackle {
		t.Fatalf("a 300 ms digital hole inside speech must fail: %+v", a)
	}
	if a.LongestZeroHole < 299 || a.LongestZeroHole > 301 {
		t.Fatalf("longest_zero_hole_ms = %v, want ~300", a.LongestZeroHole)
	}
}

func TestScoreSegmentLeadsAndTrailingSilenceAreNotHoles(t *testing.T) {
	t.Parallel()
	lead := make([]float32, 24_000*70/1000) // Kokoro-sized 70 ms lead
	var samples []float32
	samples = append(samples, lead...)
	samples = append(samples, synthSine(24_000, 12_000, 440, 0.5)...)
	samples = append(samples, lead...) // second segment's lead at the join
	samples = append(samples, synthSine(24_000, 12_000, 440, 0.5)...)
	samples = append(samples, make([]float32, 24_000)...) // a second of trailing zeros
	a := scoreSamples("segments.wav", samples, 24_000)
	if a.HasCrackle {
		t.Fatalf("segment leads and trailing silence must pass: %+v", a)
	}
	if a.ZeroHoleRuns != 1 || a.LongestZeroHole < 69 || a.LongestZeroHole > 71 {
		t.Fatalf("only the join counts as a run: runs=%d longest=%v", a.ZeroHoleRuns, a.LongestZeroHole)
	}
}

func TestScoreClippingFails(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 8_000, 440, 1.0)
	a := scoreSamples("clipped.wav", samples, 24_000)
	if !a.HasCrackle || !a.Clipped {
		t.Fatalf("a full-scale peak must fail as clipping: %+v", a)
	}
}

func TestScoreIgnoresLeadingTrailingSilence(t *testing.T) {
	t.Parallel()
	tone := synthSine(24_000, 2_000, 440, 0.5)
	samples := make([]float32, 0, 2_400)
	samples = append(samples, make([]float32, 200)...)
	samples = append(samples, tone...)
	samples = append(samples, make([]float32, 200)...)
	a := scoreSamples("pad.wav", samples, 24_000)
	if a.HasCrackle {
		t.Fatalf("leading/trailing silence flagged as crackle: %+v", a)
	}
	if a.LeadingSilenceS < 0.005 || a.TrailingSilenceS < 0.005 {
		t.Fatalf("edge silence not recorded: lead=%v trail=%v", a.LeadingSilenceS, a.TrailingSilenceS)
	}
}

func TestAnalyzeRunDirRescoresPlayback(t *testing.T) {
	runDir := t.TempDir()
	wavDir := filepath.Join(runDir, "audio", "01-chat")
	if err := os.MkdirAll(wavDir, 0o700); err != nil {
		t.Fatal(err)
	}
	samples := synthSine(24_000, 2_400, 440, 0.5)
	wavPath := filepath.Join(wavDir, "playback.wav")
	if err := audio.WriteWAVFloat32(wavPath, 24_000, samples); err != nil {
		t.Fatal(err)
	}
	if err := analyzeRunDir(runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wavDir, "analysis.json")); err != nil {
		t.Fatalf("analysis.json: %v", err)
	}
}

func TestAnalyzeRunDirFailsOnCrackle(t *testing.T) {
	runDir := t.TempDir()
	wavDir := filepath.Join(runDir, "audio", "01-chat")
	if err := os.MkdirAll(wavDir, 0o700); err != nil {
		t.Fatal(err)
	}
	samples := synthSine(24_000, 8_000, 440, 0.4)
	for i := 0; i < 12; i++ {
		off := 400 + i*500
		samples[off] = 1
		samples[off+1] = -1
	}
	if err := audio.WriteWAVFloat32(filepath.Join(wavDir, "playback.wav"), 24_000, samples); err != nil {
		t.Fatal(err)
	}
	if err := analyzeRunDir(runDir); err == nil {
		t.Fatal("want crackle failure")
	}
}

func TestAnalyzeMissingDir(t *testing.T) {
	t.Parallel()
	if err := analyzeRunDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want error for missing run dir")
	}
}
