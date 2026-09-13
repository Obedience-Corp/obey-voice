package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/aecprobe"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestNormalizeAcousticTextIgnoresCaseAndPunctuation(t *testing.T) {
	if got := normalizeAcousticText("  Hello, Samantha! "); got != "hello samantha" {
		t.Fatalf("normalized transcript = %q", got)
	}
	if normalizeAcousticText("hello samantha") != normalizeAcousticText("HELLO, SAMANTHA.") {
		t.Fatal("equivalent transcript spellings did not normalize equally")
	}
}

func TestAcousticRunRejectsUnsafeInputsBeforeOpeningDevices(t *testing.T) {
	if err := (*liveRunner)(nil).runAcoustic(context.Background(), "speech.wav", "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent is not configured") {
		t.Fatalf("nil runner error = %v", err)
	}
}

func TestAcousticEnvelopeCorrelationRejectsSilenceAndClipping(t *testing.T) {
	stimulus := make([]float32, 4*audio.ChunkSize)
	for i := range stimulus {
		stimulus[i] = float32((i/audio.ChunkSize)%4+1) * 0.04
	}
	if got := acousticEnvelopeCorrelation(stimulus, make([]float32, len(stimulus)), audio.SampleRate/100, 20); got != 0 {
		t.Fatalf("silence envelope correlation = %.3f", got)
	}
	if peak := aecprobe.PeakLevel([]float32{0.2, 1.0}); peak < 0.999 {
		t.Fatalf("clipping peak = %.3f", peak)
	}
}

func TestAcousticEnvelopeCorrelationSeparatesRecordedFixtures(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_BENCH_FIXTURES")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("home directory unavailable")
		}
		root = filepath.Join(home, ".cache", "festival-voice", "fixtures", "bench")
	}
	paths := make([][]float32, 3)
	for i := range paths {
		source, err := audio.NewFixtureSourceFromWAV(filepath.Join(root, "synth-0"+string(rune('1'+i))+".wav"), audio.ChunkSize, false)
		if err != nil {
			t.Skipf("recorded benchmark fixtures unavailable: %v", err)
		}
		for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
			paths[i] = append(paths[i], chunk...)
		}
		_ = source.Close()
	}
	good := acousticEnvelopeCorrelation(paths[0], append(make([]float32, audio.SampleRate/2), paths[0]...), audio.SampleRate/100, 100)
	if good < acousticMinEnvelopeCorrelation {
		t.Fatalf("same fixture correlation = %.3f, want >= %.3f", good, acousticMinEnvelopeCorrelation)
	}
	for i := 1; i < len(paths); i++ {
		common := min(len(paths[0]), len(paths[i]))
		if got := acousticEnvelopeCorrelation(paths[0][:common], paths[i][:common], audio.SampleRate/100, 100); got >= acousticMinEnvelopeCorrelation {
			t.Fatalf("wrong fixture %d correlation = %.3f, want < %.3f", i+1, got, acousticMinEnvelopeCorrelation)
		}
	}
}

func TestAcousticTranscriptNumberFormattingPreservesMeaning(t *testing.T) {
	if normalizeAcousticText("What is two plus two?") != normalizeAcousticText("What is 2 plus 2?") {
		t.Fatal("equivalent spoken numbers differ")
	}
	for _, wrong := range []string{"What is two plus three?", "What is not two plus two?", "What is two?"} {
		if normalizeAcousticText(wrong) == normalizeAcousticText("What is two plus two?") {
			t.Fatalf("changed meaning accepted: %q", wrong)
		}
	}
}

// An opt-in offline check verifies that the saved audio reproduces the gate's
// score. It never opens a device or starts an inference provider.
func TestAcousticSavedCaptureReproducesScore(t *testing.T) {
	dir := os.Getenv("OBEY_VOICE_ACOUSTIC_CAPTURE")
	if dir == "" {
		t.Skip("set OBEY_VOICE_ACOUSTIC_CAPTURE to an acoustic artifact directory")
	}
	source, err := audio.NewFixtureSourceFromWAV(filepath.Join(dir, "stimulus.wav"), audio.ChunkSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var reference []float32
	for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
		reference = append(reference, chunk...)
	}
	mic, rate, err := audio.ReadWAVFloat32(filepath.Join(dir, "mic-in.wav"))
	if err != nil || rate != audio.SampleRate {
		t.Fatalf("microphone rate=%d err=%v", rate, err)
	}
	score := acousticEnvelopeCorrelation(reference, mic, audio.SampleRate/100, 200)
	t.Logf("saved microphone envelope correlation %.6f", score)
	if score < acousticMinEnvelopeCorrelation {
		t.Fatalf("saved capture confidence %.6f below %.2f", score, acousticMinEnvelopeCorrelation)
	}
}
