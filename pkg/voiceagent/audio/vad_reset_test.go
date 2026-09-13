package audio

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// This opt-in regression exercises the installed native detector, without
// microphone/speaker devices or transcription. A fake whose Clear already
// resets offsets cannot detect the original wrapper-to-native API mismatch.
func TestVADClearRestartsNativeTimeline(t *testing.T) {
	modelsDir := os.Getenv("OBEY_VOICE_VAD_RESET_MODELS_DIR")
	fixture := os.Getenv("OBEY_VOICE_VAD_RESET_FIXTURE")
	if modelsDir == "" || fixture == "" {
		t.Skip("set OBEY_VOICE_VAD_RESET_MODELS_DIR and OBEY_VOICE_VAD_RESET_FIXTURE for native VAD proof")
	}
	if _, err := os.Stat(filepath.Join(modelsDir, "silero_vad.onnx")); err != nil {
		t.Fatal(err)
	}
	source, err := NewFixtureSourceFromWAV(fixture, vadWindowSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Errorf("close fixture source: %v", err)
		}
	}()
	samples := make([]float32, SampleRate/2)
	for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
		samples = append(samples, chunk...)
	}
	samples = append(samples, make([]float32, 2*SampleRate)...)
	vad, err := NewVAD(&config.Config{ModelsDir: modelsDir, VADSilenceDuration: 1.2})
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()
	consume := func() ([]float32, int) {
		for i := 0; i < len(samples); i += vadWindowSize {
			vad.AcceptWaveform(samples[i:min(i+vadWindowSize, len(samples))])
		}
		vad.Flush()
		if vad.IsEmpty() {
			t.Fatal("fixture did not produce a native speech segment")
		}
		return vad.FrontSegment()
	}
	first, firstStart := consume()
	vad.Clear()
	if !vad.IsEmpty() {
		t.Fatal("Clear left queued native speech")
	}
	second, secondStart := consume()
	t.Logf("native VAD repeated stream: first start=%d samples=%d; second start=%d samples=%d", firstStart, len(first), secondStart, len(second))
	if secondStart != firstStart || !slices.Equal(second, first) {
		t.Fatalf("Clear did not restart native timeline/state: first start=%d samples=%d; second start=%d samples=%d", firstStart, len(first), secondStart, len(second))
	}
}
