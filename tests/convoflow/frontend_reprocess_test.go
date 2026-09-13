package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// reprocessedFrontend retains actual ProcessCapture output. Merely replaying
// reference/capture operations without retaining that return value would score
// the original capture again and could falsely credit a candidate DSP change.
type reprocessedFrontend struct {
	audio.Frontend
	samples      []float32
	captureSizes []int
	closed       bool
}

func (f *reprocessedFrontend) ProcessCapture(samples []float32) []float32 {
	f.captureSizes = append(f.captureSizes, len(samples))
	out := f.Frontend.ProcessCapture(samples)
	f.samples = append(f.samples, out...)
	return out
}

func (f *reprocessedFrontend) SetReferenceDelay(samples int) {
	if delayed, ok := f.Frontend.(audio.ReferenceDelayer); ok {
		delayed.SetReferenceDelay(samples)
	}
}

func (f *reprocessedFrontend) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return f.Frontend.Close()
}

// TestReprocessSavedFrontendTrace is an opt-in, offline diagnostic. Supply the
// original run root in OBEY_VOICE_FRONTEND_REPROCESS_RUN and an explicit config
// in OBEY_VOICE_VAD_CONFIG. It opens only the frontend and installed Silero VAD,
// never a device or STT/LLM/TTS provider. Success means replay completed; the
// logged candidate_detects_stimulus and candidate_rejects_own_playback are the
// separate DSP criteria, and neither constitutes physical acceptance.
func TestReprocessSavedFrontendTrace(t *testing.T) {
	runDir := os.Getenv("OBEY_VOICE_FRONTEND_REPROCESS_RUN")
	if runDir == "" {
		t.Skip("set OBEY_VOICE_FRONTEND_REPROCESS_RUN to a saved interruption run root")
	}
	configPath := os.Getenv("OBEY_VOICE_VAD_CONFIG")
	if configPath == "" {
		t.Fatal("set OBEY_VOICE_VAD_CONFIG to the explicit replay configuration")
	}
	cfg, err := loadFixtureConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if models := os.Getenv("OBEY_VOICE_VAD_MODELS"); models != "" {
		cfg.ModelsDir = models
	}
	artifactDir := filepath.Join(runDir, "acoustic", "interruption")
	arm, stimulus, err := savedInterruptionOffsets(runDir, artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := interruptionFrontend(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reprocessed := &reprocessedFrontend{Frontend: frontend}
	defer reprocessed.Close()
	if err := replayFrontendTrace(filepath.Join(artifactDir, "frontend.jsonl"), reprocessed); err != nil {
		t.Fatal(err)
	}
	original, rate, err := audio.ReadWAVFloat32(filepath.Join(artifactDir, "mic-in.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if rate != audio.SampleRate || len(original) != len(reprocessed.samples) || len(original) == 0 {
		t.Fatalf("trace/capture mismatch: raw=%d samples at %d Hz, reprocessed=%d", len(original), rate, len(reprocessed.samples))
	}
	diagnosticDir, err := os.MkdirTemp(artifactDir, "frontend-reprocess-")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(diagnosticDir, "mic-reprocessed.wav")
	if err := audio.WriteWAVFloat32(output, audio.SampleRate, reprocessed.samples); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	fmt.Fprintf(&log, "Offline diagnostic only; no physical acceptance claim.\nconfig=%s\noutput=%s\n", configPath, output)
	fmt.Fprintf(&log, "actual capture callbacks=%d first_size=%d samples; VAD receives these original frame boundaries.\n", len(reprocessed.captureSizes), reprocessed.captureSizes[0])
	fmt.Fprintf(&log, "capture start approximated by first STTPhase/listening; no absolute capture timestamp is in the trace. arm=response_started+600ms => %.6fs; stimulus_started => %.6fs. Boundary uncertainty is not measured.\n", arm, stimulus)
	names := []string{"mic-in.wav", "mic-processed.wav", "mic-reprocessed.wav"}
	// Optional stage WAVs are created by a separate local DSP diagnostic.
	// Explicit names avoid accidentally scoring unrelated private recordings.
	for _, name := range strings.Split(os.Getenv("OBEY_VOICE_VAD_STAGE_FILES"), ",") {
		if name != "" {
			if filepath.Base(name) != name || !strings.HasSuffix(name, ".wav") {
				t.Fatalf("stage file must be a WAV basename: %q", name)
			}
			names = append(names, name)
		}
	}
	for _, name := range names {
		path := filepath.Join(artifactDir, name)
		if name == "mic-reprocessed.wav" {
			path = output
		}
		d, err := replayBargeVAD(cfg, path, 0.85, arm, reprocessed.captureSizes...)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&log, formatVADDiagnostic(d))
		pre, post := d.SpansIn(arm, stimulus), d.SpansIn(stimulus, d.Seconds)
		fmt.Fprintf(&log, "%s armed_before_stimulus=%v stimulus_onward=%v\n", name, pre, post)
		if name == "mic-reprocessed.wav" {
			fmt.Fprintf(&log, "candidate_detects_stimulus=%t candidate_rejects_own_playback=%t\n", len(post) > 0, len(pre) == 0)
		}
	}
	logPath := filepath.Join(diagnosticDir, "frontend-reprocess.log")
	if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log(log.String())
	t.Logf("retained diagnostic: %s", logPath)
}

func savedInterruptionOffsets(runDir, artifactDir string) (arm, stimulus float64, err error) {
	body, err := os.ReadFile(filepath.Join(artifactDir, "interruption.json"))
	if err != nil {
		return 0, 0, err
	}
	var report struct {
		ResponseStarted time.Time `json:"response_started"`
		StimulusStarted time.Time `json:"stimulus_started"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return 0, 0, err
	}
	file, err := os.Open(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var event struct {
			At    time.Time              `json:"at"`
			Type  string                 `json:"type"`
			Event struct{ Phase string } `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return 0, 0, err
		}
		if event.Type != "STTPhase" || event.Event.Phase != "listening" {
			continue
		}
		if event.At.IsZero() || report.ResponseStarted.IsZero() || report.StimulusStarted.IsZero() {
			return 0, 0, fmt.Errorf("missing interruption timeline timestamps")
		}
		arm = report.ResponseStarted.Add(600 * time.Millisecond).Sub(event.At).Seconds()
		stimulus = report.StimulusStarted.Sub(event.At).Seconds()
		if arm < 0 || stimulus <= arm {
			return 0, 0, fmt.Errorf("invalid capture-relative timeline: arm %.6f, stimulus %.6f", arm, stimulus)
		}
		return arm, stimulus, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, fmt.Errorf("events.jsonl has no STTPhase/listening timestamp")
}
