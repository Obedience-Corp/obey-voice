package audio

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// These opt-in regressions run the installed Silero model. They are the proof
// behind the file runner's timing: a detector that is never cleared reports
// every segment's start as an absolute sample index on the file's timeline,
// including past its own 30 s internal buffer, and a segment longer than
// MaxSpeech is split into consecutive spans instead of being truncated.
// A fake cannot prove either: both live in the native detector.

type nativeSpan struct {
	start   int
	samples int
}

func fileVADModelsDir(t *testing.T) string {
	t.Helper()
	modelsDir := os.Getenv("OBEY_VOICE_FILE_VAD_MODELS_DIR")
	if modelsDir == "" {
		t.Skip("set OBEY_VOICE_FILE_VAD_MODELS_DIR for the native file VAD proof")
	}
	if _, err := os.Stat(filepath.Join(modelsDir, "silero_vad.onnx")); err != nil {
		t.Fatal(err)
	}
	return modelsDir
}

// fileVADUtterance loads tracked real speech that Silero accepts at the
// production 0.6 threshold. The default is docs/audio/golden/N2/before.wav
// (24 kHz Kokoro speech, resampled in-process to SampleRate). The voiceflow
// utterance.wav fixture is not used: it emits zero spans at 0.6.
// OBEY_VOICE_FILE_VAD_FIXTURE still overrides the path.
func fileVADUtterance(t *testing.T) []float32 {
	t.Helper()
	path := os.Getenv("OBEY_VOICE_FILE_VAD_FIXTURE")
	if path == "" {
		path = filepath.Join("..", "..", "..", "docs", "audio", "golden", "N2", "before.wav")
	}
	samples, rate, err := ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	if rate != SampleRate {
		samples = ResampleLinear(samples, rate, SampleRate)
	}
	if len(samples) == 0 {
		t.Fatalf("fixture %s produced no samples", path)
	}
	return samples
}

// collectFileVADSpans feeds every sample once, in detector-window pieces, then
// flushes and drains. The detector is never cleared, which is the contract the
// file runner relies on.
func collectFileVADSpans(t *testing.T, vad *VAD, samples []float32) []nativeSpan {
	t.Helper()
	for i := 0; i < len(samples); i += vadWindowSize {
		vad.AcceptWaveform(samples[i:min(i+vadWindowSize, len(samples))])
	}
	vad.Flush()

	var spans []nativeSpan
	for !vad.IsEmpty() {
		seg, start := vad.FrontSegment()
		vad.Pop()
		spans = append(spans, nativeSpan{start: start, samples: len(seg)})
	}
	return spans
}

func TestFileVADKeepsOffsetsAbsoluteAcrossTheBuffer(t *testing.T) {
	modelsDir := fileVADModelsDir(t)
	utterance := fileVADUtterance(t)

	const total = 75 * SampleRate
	markers := []int{5 * SampleRate, 40 * SampleRate, 70 * SampleRate}
	stream := make([]float32, total)
	for _, at := range markers {
		copy(stream[at:], utterance)
	}

	vad, err := NewFileVAD(&config.Config{ModelsDir: modelsDir}, FileVADOptions{})
	if err != nil {
		t.Fatalf("NewFileVAD: %v", err)
	}
	defer vad.Delete()

	spans := collectFileVADSpans(t, vad, stream)
	for i, span := range spans {
		t.Logf("span %d: start=%d (%.3fs) samples=%d", i, span.start, float64(span.start)/SampleRate, span.samples)
	}
	if len(spans) != len(markers) {
		t.Fatalf("got %d spans, want one per marker (%d)", len(spans), len(markers))
	}

	const tolerance = 300 * SampleRate / 1000 // 300ms
	for i, want := range markers {
		got := spans[i].start
		if got < want-tolerance || got > want+tolerance {
			t.Errorf("span %d starts at sample %d (%.3fs), want within 300ms of %d (%.3fs)",
				i, got, float64(got)/SampleRate, want, float64(want)/SampleRate)
		}
	}
	if last := spans[len(spans)-1].start; last < vadBufferSeconds*SampleRate {
		t.Errorf("last span start %d is inside the %ds detector buffer; the test did not exercise absolute offsets", last, vadBufferSeconds)
	}
}

func TestFileVADSplitsLongSpeechIntoAdjacentSpans(t *testing.T) {
	modelsDir := fileVADModelsDir(t)
	utterance := fileVADUtterance(t)

	// At least 30 s of speech with no gap longer than the fixture's own pauses,
	// ahead of a MinSilence far larger than those pauses: only MaxSpeech can
	// split it. Repeats are derived from the loaded fixture length so a shorter
	// clip still fills 30 s.
	const (
		lead      = SampleRate / 2
		maxSpeech = 10 * time.Second
	)
	repeats := (30*SampleRate + len(utterance) - 1) / len(utterance)
	stream := make([]float32, lead, lead+repeats*len(utterance)+SampleRate)
	for range repeats {
		stream = append(stream, utterance...)
	}
	stream = append(stream, make([]float32, SampleRate)...)
	speechStart, speechEnd := lead, lead+repeats*len(utterance)

	vad, err := NewFileVAD(&config.Config{ModelsDir: modelsDir}, FileVADOptions{
		MinSilence: 3 * time.Second,
		MaxSpeech:  maxSpeech,
	})
	if err != nil {
		t.Fatalf("NewFileVAD: %v", err)
	}
	defer vad.Delete()

	spans := collectFileVADSpans(t, vad, stream)
	for i, span := range spans {
		t.Logf("span %d: start=%d (%.3fs) samples=%d (%.3fs)",
			i, span.start, float64(span.start)/SampleRate, span.samples, float64(span.samples)/SampleRate)
	}
	if len(spans) < 2 {
		t.Fatalf("got %d span(s) for %.1fs of continuous speech with MaxSpeech %s: the detector did not split it",
			len(spans), float64(speechEnd-speechStart)/SampleRate, maxSpeech)
	}

	limit := int(maxSpeech.Seconds()*SampleRate) + SampleRate // one second of slack
	prevEnd := 0
	for i, span := range spans {
		if span.start < prevEnd {
			t.Errorf("span %d starts at %d, inside the previous span which ended at %d", i, span.start, prevEnd)
		}
		if span.samples > limit {
			t.Errorf("span %d is %d samples (%.3fs), longer than MaxSpeech %s", i, span.samples, float64(span.samples)/SampleRate, maxSpeech)
		}
		prevEnd = span.start + span.samples
	}
	if first := spans[0].start; first < speechStart-SampleRate/2 {
		t.Errorf("first span starts at %d, before the speech at %d", first, speechStart)
	}
	if prevEnd < speechEnd-SampleRate {
		t.Errorf("last span ends at %d, more than one second before the speech at %d", prevEnd, speechEnd)
	}
	if prevEnd > speechEnd+SampleRate {
		t.Errorf("last span ends at %d, past the speech at %d", prevEnd, speechEnd)
	}
}
