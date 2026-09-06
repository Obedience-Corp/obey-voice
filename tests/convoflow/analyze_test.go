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
	samples[2000] = 1
	samples[2001] = -1
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
		samples[off] = 1
		samples[off+1] = -1
	}
	a := scoreSamples("clicks.wav", samples, 24_000)
	if !a.HasCrackle {
		t.Fatalf("12 clicks should exceed MaxImpulses=8: %+v", a)
	}
}

func TestScoreMidSilenceHoleFails(t *testing.T) {
	t.Parallel()
	samples := synthSine(24_000, 8_000, 440, 0.5)
	// Three interior holes → MidSilenceRuns > 2.
	for _, start := range []int{1500, 3000, 4500} {
		for i := start; i < start+64; i++ {
			samples[i] = 0
		}
	}
	a := scoreSamples("holes.wav", samples, 24_000)
	if !a.HasCrackle {
		t.Fatalf("three mid-speech holes should fail: %+v", a)
	}
	if a.MidSilenceRuns < 3 {
		t.Fatalf("MidSilenceRuns = %d, want ≥ 3", a.MidSilenceRuns)
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
