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
	inputDir := os.Getenv("OBEY_VOICE_FRONTEND_REPROCESS_RUN")
	if inputDir == "" {
		t.Skip("set OBEY_VOICE_FRONTEND_REPROCESS_RUN to a saved interruption run root or acoustic artifact directory")
	}
	runDir, artifactDir, err := resolveFrontendReprocessArtifacts(inputDir)
	if err != nil {
		t.Fatal(err)
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
	arm, stimulus, timingMethod, err := savedInterruptionOffsetsDetailed(runDir, artifactDir)
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
	fmt.Fprintf(&log, "capture timing mapping=%s; offsets use saved report response/stimulus timestamps and are host observations, not ADC/speaker timestamps. arm=response_started+600ms => %.6fs; stimulus_started => %.6fs.\n", timingMethod, arm, stimulus)
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

// resolveFrontendReprocessArtifacts accepts the historical run-root layout
// and a direct acoustic artifact directory. A directory that looks like a
// direct artifact is validated instead of silently falling back to another
// path, so a partial capture cannot produce a misleading replay.
func resolveFrontendReprocessArtifacts(inputDir string) (runDir, artifactDir string, err error) {
	info, err := os.Stat(inputDir)
	if err != nil {
		return "", "", fmt.Errorf("frontend replay input %s: %w", inputDir, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("frontend replay input %s is not a directory", inputDir)
	}
	looksDirect := false
	for _, name := range []string{"frontend.jsonl", "interruption.json", "mic-in.wav"} {
		if _, statErr := os.Stat(filepath.Join(inputDir, name)); statErr == nil {
			looksDirect = true
			break
		} else if !os.IsNotExist(statErr) {
			return "", "", fmt.Errorf("inspect frontend replay input %s: %w", inputDir, statErr)
		}
	}
	if looksDirect {
		for _, name := range []string{"frontend.jsonl", "interruption.json", "mic-in.wav"} {
			if _, statErr := os.Stat(filepath.Join(inputDir, name)); statErr != nil {
				return "", "", fmt.Errorf("direct frontend artifact %s missing %s: %w", inputDir, name, statErr)
			}
		}
		return inputDir, inputDir, nil
	}
	artifactDir = filepath.Join(inputDir, "acoustic", "interruption")
	if _, err := os.Stat(artifactDir); err != nil {
		return "", "", fmt.Errorf("legacy frontend artifact directory %s: %w", artifactDir, err)
	}
	return inputDir, artifactDir, nil
}

func savedInterruptionOffsets(runDir, artifactDir string) (arm, stimulus float64, err error) {
	arm, stimulus, _, err = savedInterruptionOffsetsDetailed(runDir, artifactDir)
	return arm, stimulus, err
}

type savedCaptureTiming struct {
	ObservedAt time.Time
	Samples    int
}

func savedInterruptionOffsetsDetailed(runDir, artifactDir string) (arm, stimulus float64, method string, err error) {
	body, err := os.ReadFile(filepath.Join(artifactDir, "interruption.json"))
	if err != nil {
		return 0, 0, "", err
	}
	var report struct {
		ResponseStarted time.Time `json:"response_started"`
		StimulusStarted time.Time `json:"stimulus_started"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return 0, 0, "", err
	}
	tracePath := filepath.Join(artifactDir, "frontend.jsonl")
	trace, traceErr := savedTraceCaptureTimings(tracePath)
	if traceErr == nil {
		allTimed := true
		allUntimed := true
		for _, callback := range trace {
			allTimed = allTimed && !callback.ObservedAt.IsZero()
			allUntimed = allUntimed && callback.ObservedAt.IsZero()
		}
		if !allTimed && !allUntimed {
			return 0, 0, "", fmt.Errorf("frontend trace %s mixes timed and untimed capture callbacks", tracePath)
		}
		if allTimed {
			arm, err = traceRelativeOffset(trace, report.ResponseStarted.Add(600*time.Millisecond))
			if err != nil {
				return 0, 0, "", err
			}
			stimulus, err = traceRelativeOffset(trace, report.StimulusStarted)
			if err != nil {
				return 0, 0, "", err
			}
			if report.ResponseStarted.IsZero() || report.StimulusStarted.IsZero() {
				return 0, 0, "", fmt.Errorf("missing interruption timeline timestamps")
			}
			return arm, stimulus, "recorded frontend capture callback timestamps", nil
		}
	} else if !os.IsNotExist(traceErr) {
		return 0, 0, "", traceErr
	}
	anchor, err := savedListeningAnchor(filepath.Join(runDir, "events.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, "", err
	}
	if err != nil {
		return 0, 0, "", fmt.Errorf("no saved listening anchor for frontend replay: %w", err)
	}
	if anchor.IsZero() || report.ResponseStarted.IsZero() || report.StimulusStarted.IsZero() {
		return 0, 0, "", fmt.Errorf("missing interruption timeline timestamps")
	}
	arm = report.ResponseStarted.Add(600 * time.Millisecond).Sub(anchor).Seconds()
	stimulus = report.StimulusStarted.Sub(anchor).Seconds()
	if arm < 0 || stimulus < 0 {
		return 0, 0, "", fmt.Errorf("invalid capture-relative timeline: arm %.6f, stimulus %.6f", arm, stimulus)
	}
	if traceErr == nil {
		return arm, stimulus, "legacy frontend trace without timestamps plus STTPhase/listening anchor (approximate)", nil
	}
	return arm, stimulus, "legacy STTPhase/listening anchor (approximate)", nil
}

func savedListeningAnchor(path string) (time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
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
			return time.Time{}, err
		}
		if event.Type != "STTPhase" || event.Event.Phase != "listening" {
			continue
		}
		if event.At.IsZero() {
			return time.Time{}, fmt.Errorf("events.jsonl listening timestamp is zero")
		}
		return event.At, nil
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, fmt.Errorf("events.jsonl has no STTPhase/listening timestamp")
}

func savedTraceCaptureTimings(path string) ([]savedCaptureTiming, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var expected uint64
	var captures []savedCaptureTiming
	for scanner.Scan() {
		var event frontendTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode frontend trace %s: %w", path, err)
		}
		if event.Seq != expected+1 {
			return nil, fmt.Errorf("frontend trace %s sequence %d after %d", path, event.Seq, expected)
		}
		expected = event.Seq
		if event.Op == "capture" {
			if len(event.Samples) == 0 {
				return nil, fmt.Errorf("frontend trace %s capture event has no samples", path)
			}
			captures = append(captures, savedCaptureTiming{ObservedAt: event.ObservedAt, Samples: len(event.Samples)})
		} else if event.Op != "reference" && event.Op != "delay" && event.Op != "close" {
			return nil, fmt.Errorf("frontend trace %s unknown operation %q", path, event.Op)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(captures) == 0 {
		return nil, fmt.Errorf("frontend trace %s has no capture events", path)
	}
	return captures, nil
}

func traceRelativeOffset(captures []savedCaptureTiming, target time.Time) (float64, error) {
	if target.IsZero() {
		return 0, fmt.Errorf("missing interruption timeline timestamp")
	}
	var sampleOffset int
	for _, callback := range captures {
		if !callback.ObservedAt.Before(target) {
			return float64(sampleOffset) / float64(audio.SampleRate), nil
		}
		sampleOffset += callback.Samples
	}
	return 0, fmt.Errorf("timeline target %s has no recorded capture callback at or after it", target.Format(time.RFC3339Nano))
}

func TestResolveFrontendReprocessArtifactsAcceptsDirectDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frontend.jsonl", "interruption.json", "mic-in.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runDir, artifactDir, err := resolveFrontendReprocessArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runDir != dir || artifactDir != dir {
		t.Fatalf("resolved run=%q artifact=%q, want both %q", runDir, artifactDir, dir)
	}
}

func TestResolveFrontendReprocessArtifactsRejectsPartialDirectDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frontend.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveFrontendReprocessArtifacts(dir); err == nil {
		t.Fatal("partial direct artifact directory was accepted")
	}
}

func TestSavedInterruptionOffsetsAllowStimulusBeforeArm(t *testing.T) {
	dir := t.TempDir()
	anchor := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	report := struct {
		ResponseStarted time.Time `json:"response_started"`
		StimulusStarted time.Time `json:"stimulus_started"`
	}{anchor.Add(100 * time.Millisecond), anchor.Add(200 * time.Millisecond)}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interruption.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	trace := fmt.Sprintf("{\"seq\":1,\"op\":\"capture\",\"observed_at\":%q,\"samples\":[0,0,0,0]}\n{\"seq\":2,\"op\":\"capture\",\"observed_at\":%q,\"samples\":[0,0,0,0,0,0]}\n{\"seq\":3,\"op\":\"capture\",\"observed_at\":%q,\"samples\":[0,0]}\n", anchor.Format(time.RFC3339Nano), anchor.Add(200*time.Millisecond).Format(time.RFC3339Nano), anchor.Add(700*time.Millisecond).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "frontend.jsonl"), []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	arm, stimulus, err := savedInterruptionOffsets(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if arm != 10.0/16000.0 || stimulus != 4.0/16000.0 {
		t.Fatalf("offsets arm=%.6f stimulus=%.6f, want callback starts arm=%.6f stimulus=%.6f", arm, stimulus, 10.0/16000.0, 4.0/16000.0)
	}
}

func TestSavedInterruptionOffsetsRejectNegativeCaptureRelativeTime(t *testing.T) {
	dir := t.TempDir()
	anchor := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	report := struct {
		ResponseStarted time.Time `json:"response_started"`
		StimulusStarted time.Time `json:"stimulus_started"`
	}{anchor.Add(-2 * time.Second), anchor.Add(-time.Second)}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interruption.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	events := `{"type":"STTPhase","event":{"phase":"listening"},"at":"2026-09-15T12:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := savedInterruptionOffsets(dir, dir); err == nil {
		t.Fatal("negative capture-relative timing was accepted")
	}
}
