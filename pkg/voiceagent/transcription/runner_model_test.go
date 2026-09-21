package transcription

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// fixtureUtterance is tracked Kokoro speech that Silero accepts at the
// production FileVAD threshold of 0.6: docs/audio/golden/N2/before.wav,
// 24 kHz, resampled in-process to SampleRate. The voiceflow clip
// tests/voiceflow/testdata/utterance.wav is not used: it emits zero spans
// at 0.6.
const fixtureUtterance = "../../../docs/audio/golden/N2/before.wav"

// TestRunPlacesRealSpeechOnTheFileTimeline is the opt-in proof that the runner's
// times are positions in the file and not positions in the decode. It builds a
// file whose speech starts at known offsets, runs the real VAD and the real
// recognizer over it, and checks where the segments landed.
func TestRunPlacesRealSpeechOnTheFileTimeline(t *testing.T) {
	modelsDir := os.Getenv("OBEY_VOICE_TRANSCRIBE_MODELS_DIR")
	if modelsDir == "" {
		t.Skip("set OBEY_VOICE_TRANSCRIBE_MODELS_DIR for the real-model transcription smoke test")
	}
	if _, err := os.Stat(filepath.Join(modelsDir, "silero_vad.onnx")); err != nil {
		t.Fatal(err)
	}

	utterance, rate, err := audio.ReadWAVFloat32(fixtureUtterance)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixtureUtterance, err)
	}
	if rate != audio.SampleRate {
		utterance = audio.ResampleLinear(utterance, rate, audio.SampleRate)
	}
	if len(utterance) == 0 {
		t.Fatalf("fixture %s produced no samples", fixtureUtterance)
	}

	// 3 s silence, speech, 2 s silence, the same speech again. The first
	// start stays at 3 s. The second is derived from 3 s + the resampled
	// clip + 2 s because N2 is 24 kHz Kokoro speech (~1.289 s after
	// resample to 16 kHz), not the 1.5 s 16 kHz voiceflow fixture the
	// task named. SamplesToMicros converts that sample index so the
	// expected offset matches the runner's integer timeline.
	const (
		leadSamples = 3 * audio.SampleRate
		gapSamples  = 2 * audio.SampleRate
	)
	var samples []float32
	samples = append(samples, make([]float32, leadSamples)...)
	samples = append(samples, utterance...)
	samples = append(samples, make([]float32, gapSamples)...)
	samples = append(samples, utterance...)
	path := filepath.Join(t.TempDir(), "two-utterances.wav")
	if err := audio.WriteWAVFloat32(path, audio.SampleRate, samples); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ModelsDir:          modelsDir,
		STTProvider:        "sherpa-parakeet",
		ParakeetModel:      config.DefaultParakeetModel,
		VADEnabled:         true,
		VADSilenceDuration: 0.6,
		VADPreRollMS:       300,
	}

	source, err := audio.NewWAVFileSource(path, audio.ChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	}()

	opts := audio.FileVADOptions{MinSilence: 600 * time.Millisecond, MaxSpeech: 20 * time.Second}
	vad, err := audio.NewFileVAD(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()

	detector := NewFileSpanDetector(vad, opts, time.Duration(cfg.VADPreRollMS)*time.Millisecond)

	decoder, info, cleanup, err := stt.NewSegmentDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	t.Logf("decoder provider=%s mode=%s model=%s revision=%s", info.Provider, info.Mode, info.ModelID, info.ModelRevision)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	sink := &fakeSink{}
	summary, err := Run(ctx, Request{JobID: "smoke-1", Language: "en", Timing: TimingUtterance},
		Deps{Source: source, Detector: detector, Decoder: decoder, Info: info, Sink: sink, Logf: t.Logf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	segments := sink.segments()
	for _, seg := range segments {
		t.Logf("segment %s [%d,%d) us: %q", seg.ID, seg.StartUS, seg.EndUS, seg.Text)
	}
	if summary.Outcome != OutcomeSpeech || len(segments) != 2 {
		t.Fatalf("summary = %+v with %d segments, want speech with 2", summary, len(segments))
	}
	const (
		tolerance    = 500_000
		firstStartUS = 3_000_000
	)
	secondStartUS := SamplesToMicros(int64(leadSamples+len(utterance)+gapSamples), audio.SampleRate)
	for i, want := range []int64{firstStartUS, secondStartUS} {
		got := segments[i].StartUS
		if got < want-tolerance || got > want+tolerance {
			t.Errorf("segment %s starts at %d us, want within %d us of %d", segments[i].ID, got, tolerance, want)
		}
		if segments[i].Text == "" {
			t.Errorf("segment %s has no text", segments[i].ID)
		}
	}
}
