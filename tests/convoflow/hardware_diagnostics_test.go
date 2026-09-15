package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestReplaySavedBargeVAD is opt-in because it needs the installed Silero
// asset. Set OBEY_VOICE_VAD_CAPTURE to a run directory and optionally
// OBEY_VOICE_VAD_CONFIG to its explicit YAML and OBEY_VOICE_VAD_MODELS to the
// model root. Set OBEY_VOICE_VAD_STIMULUS_OFFSET to label pre-stimulus and
// stimulus windows in the log; the replay itself is independent of that label.
func TestReplaySavedBargeVAD(t *testing.T) {
	dir := os.Getenv("OBEY_VOICE_VAD_CAPTURE")
	if dir == "" {
		t.Skip("set OBEY_VOICE_VAD_CAPTURE to an acoustic artifact directory")
	}
	configPath := os.Getenv("OBEY_VOICE_VAD_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(dir, "config.resolved.yaml")
	}
	cfg, err := loadFixtureConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if models := os.Getenv("OBEY_VOICE_VAD_MODELS"); models != "" {
		cfg.ModelsDir = models
	}
	stimulus, err := diagnosticEnvFloat("OBEY_VOICE_VAD_STIMULUS_OFFSET", 0)
	if err != nil {
		t.Fatal(err)
	}
	arm, err := diagnosticEnvFloat("OBEY_VOICE_VAD_ARM_OFFSET", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("replay arm offset=%.3fs stimulus offset=%.3fs; offsets are capture-file relative and must come from the run timeline", arm, stimulus)
	wavDir := dir
	if _, err := os.Stat(filepath.Join(wavDir, "mic-in.wav")); err != nil {
		wavDir = filepath.Join(dir, "acoustic", "interruption")
	}
	samples, _, err := audio.ReadWAVFloat32(filepath.Join(wavDir, "mic-in.wav"))
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(wavDir, "frontend.jsonl")
	captureSizes, err := captureCallbackSizes(tracePath, len(samples))
	if err != nil {
		t.Fatal(err)
	}
	cadenceSource := legacyDiagnosticCadence
	if captureSizes != nil {
		cadenceSource = fmt.Sprintf("recorded frontend trace %s (%d callbacks)", tracePath, len(captureSizes))
	}
	t.Logf("replay callback cadence source=%s", cadenceSource)
	for _, name := range []string{"mic-in.wav", "mic-processed.wav"} {
		path := filepath.Join(wavDir, name)
		for _, threshold := range []float64{0.85, 0.60, 0.30} {
			d, replayErr := replayBargeVAD(cfg, path, threshold, arm, captureSizes...)
			if replayErr != nil {
				t.Fatal(replayErr)
			}
			t.Log(formatVADDiagnostic(d))
			if stimulus > 0 {
				pre := d.SpansIn(stimulus-1, stimulus)
				post := d.SpansIn(stimulus, d.Seconds)
				t.Logf("%s threshold=%.2f pre_stimulus=%v stimulus_onward=%v", name, threshold, pre, post)
			}
		}
	}
}

func (d vadDiagnostic) SpansIn(start, end float64) []vadDiagnosticSpan {
	if start < 0 {
		start = 0
	}
	result := make([]vadDiagnosticSpan, 0)
	for _, span := range d.Spans {
		if span.End > start && span.Start < end {
			result = append(result, span)
		}
	}
	return result
}

func TestDiagnosticEnvFloat(t *testing.T) {
	const key = "OBEY_VOICE_DIAGNOSTIC_FLOAT_TEST"
	t.Setenv(key, "1.25")
	got, err := diagnosticEnvFloat(key, 0)
	if err != nil || got != 1.25 {
		t.Fatalf("diagnosticEnvFloat = %v, %v", got, err)
	}
	t.Setenv(key, "bad")
	if _, err := diagnosticEnvFloat(key, 0); err == nil {
		t.Fatal("diagnosticEnvFloat accepted invalid input")
	}
}

func TestStimulusOutputDeviceOverrideIsExplicit(t *testing.T) {
	cfg := &config.Config{OutputDevice: "response-device"}
	if got := stimulusOutputDevice(cfg, ""); got != "response-device" {
		t.Fatalf("default stimulus device = %q", got)
	}
	if got := stimulusOutputDevice(cfg, "  separate-stimulus-device "); got != "separate-stimulus-device" {
		t.Fatalf("override stimulus device = %q", got)
	}
}

func TestCaptureCallbackSizesReadsRecordedBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frontend.jsonl")
	body := `{"seq":1,"op":"capture","samples":[0,0,0]}
{"seq":2,"op":"reference","samples":[1]}
{"seq":3,"op":"capture","samples":[0,0]}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sizes, err := captureCallbackSizes(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(sizes), "[3 2]"; got != want {
		t.Fatalf("capture callback sizes = %s, want %s", got, want)
	}
}

func TestCaptureCallbackSizesRejectsMalformedExistingTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frontend.jsonl")
	if err := os.WriteFile(path, []byte(`{"seq":1,"op":"capture","samples":[0]}
not-json
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureCallbackSizes(path, 1); err == nil {
		t.Fatal("malformed existing frontend trace was accepted")
	}
}

func TestCaptureCallbackSizesRejectsSequenceGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frontend.jsonl")
	body := `{"seq":1,"op":"capture","samples":[0]}
{"seq":3,"op":"capture","samples":[0]}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureCallbackSizes(path, 2); err == nil {
		t.Fatal("frontend trace sequence gap was accepted")
	}
}

func TestCaptureCallbackSizesRejectsSampleCountMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frontend.jsonl")
	body := `{"seq":1,"op":"capture","samples":[0,0]}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureCallbackSizes(path, 3); err == nil {
		t.Fatal("frontend trace sample count mismatch was accepted")
	}
}

func TestCaptureCallbackSizesMissingTraceUsesExplicitFallback(t *testing.T) {
	sizes, err := captureCallbackSizes(filepath.Join(t.TempDir(), "missing.jsonl"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if sizes != nil {
		t.Fatalf("missing trace sizes = %v, want nil for explicit legacy fallback", sizes)
	}
}
