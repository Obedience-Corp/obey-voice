package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"gopkg.in/yaml.v3"
)

type controlPopulationOptions struct {
	Output, Scenario, Config, Stimulus, Binary string
	Samples, Warmups                           int
	Timeout                                    time.Duration
}

type controlPopulationPlan struct {
	Schema    string            `json:"schema"`
	Created   time.Time         `json:"created"`
	Scenario  string            `json:"scenario"`
	Operation string            `json:"operation"`
	Samples   int               `json:"measured_samples"`
	Warmups   int               `json:"warmups"`
	Timeout   time.Duration     `json:"sample_timeout_ns"`
	Inputs    map[string]string `json:"input_sha256"`
	Build     *debug.BuildInfo  `json:"build"`
	Stimulus  string            `json:"stimulus_device_override"`
	Warmth    string            `json:"warmth_scope"`
}

type controlVoiceIdentity struct {
	Provider string `json:"provider"`
	Tier     string `json:"tier"`
	Place    string `json:"place"`
}

type controlPopulationSample struct {
	Voice     controlVoiceIdentity     `json:"actual_voice"`
	Index     int                      `json:"index"`
	Warmup    bool                     `json:"warmup"`
	Command   []string                 `json:"command"`
	Started   time.Time                `json:"started"`
	Finished  time.Time                `json:"finished"`
	Run       string                   `json:"run,omitempty"`
	Error     string                   `json:"error,omitempty"`
	Artifacts map[string]string        `json:"artifact_sha256,omitempty"`
	Timings   map[string]time.Duration `json:"timings_ns,omitempty"`
	Profile   runProfile               `json:"profile"`
	Devices   []audio.NegotiatedDevice `json:"devices,omitempty"`
}

type controlTimingSummary struct {
	Samples int           `json:"samples"`
	P50     time.Duration `json:"p50_ns"`
	P95     time.Duration `json:"p95_ns"`
}

type controlPopulationSummary struct {
	Status      string                          `json:"status"`
	Error       string                          `json:"error,omitempty"`
	Planned     int                             `json:"planned_measured_samples"`
	Measured    int                             `json:"measured_samples"`
	Samples     []controlPopulationSample       `json:"samples"`
	Timings     map[string]controlTimingSummary `json:"timings_ns,omitempty"`
	Definitions map[string]string               `json:"definitions"`
}

// Each sample is a new instance of the same executable. No build, retry, failed
// sample replacement or parallel device owner is allowed inside a population.
func runControlPopulation(ctx context.Context, o controlPopulationOptions) (runErr error) {
	return runControlPopulationWith(ctx, o, executeControlChild)
}

type controlChild func(context.Context, string, []string, string) error

func runControlPopulationWith(ctx context.Context, o controlPopulationOptions, child controlChild) (runErr error) {
	if ctx == nil || child == nil || o.Output == "" || o.Config == "" || o.Scenario == "" || o.Binary == "" {
		return errors.New("control population: context, runner, new output directory, config, scenario and binary are required")
	}
	if o.Samples < 30 || o.Warmups < 0 || o.Warmups > 100 || o.Samples > 1000 || o.Timeout <= 0 {
		return errors.New("control population: require 30..1000 measured samples, 0..100 warmups and positive timeout")
	}
	scn, err := loadScenario(o.Scenario)
	if err != nil {
		return err
	}
	if len(scn.Turns) != 1 || (scn.Turns[0].Op != "acoustic_interrupt" && scn.Turns[0].Op != "acoustic_completion") || scn.Worker.SustainedLoad {
		return errors.New("control population: requires one acoustic_interrupt or acoustic_completion turn, without sustained worker load")
	}
	turn := scn.Turns[0]
	if turn.ID == "" || filepath.Base(turn.ID) != turn.ID || turn.ID == "." || turn.ID == ".." {
		return errors.New("control population: turn ID must be a single path component")
	}
	inputs := make(map[string]string)
	paths := []string{o.Binary, o.Scenario, o.Config, turn.WAV, turn.InterruptWAV}
	if turn.Op == "acoustic_interrupt" {
		metadata := strings.TrimSuffix(turn.WAV, filepath.Ext(turn.WAV)) + ".json"
		if _, err := readAcousticMetadata(metadata); err != nil {
			return err
		}
		paths = append(paths, metadata)
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		hash, err := controlFileHash(absolute)
		if err != nil {
			return err
		}
		inputs[absolute] = hash
	}
	o.Output, err = filepath.Abs(o.Output)
	if err != nil {
		return err
	}
	o.Scenario, err = filepath.Abs(o.Scenario)
	if err != nil {
		return err
	}
	o.Config, err = filepath.Abs(o.Config)
	if err != nil {
		return err
	}
	if err := os.Mkdir(o.Output, 0700); err != nil {
		return fmt.Errorf("new population directory: %w", err)
	}
	build, _ := debug.ReadBuildInfo()
	plan := controlPopulationPlan{Schema: "acoustic-control-population.v1", Created: time.Now().UTC(), Scenario: scn.Name, Operation: turn.Op, Samples: o.Samples, Warmups: o.Warmups, Timeout: o.Timeout, Inputs: inputs, Build: build, Stimulus: o.Stimulus, Warmth: "Fresh process for every attempt; initial warmups excluded. Model-cache coldness is not measured or inferred."}
	if err := writeControlJSON(filepath.Join(o.Output, "plan.json"), plan); err != nil {
		return err
	}
	summary := controlPopulationSummary{Status: "incomplete", Planned: o.Samples, Definitions: map[string]string{
		"pause":                 "PauseForCompletion invocation to acknowledgement; excludes result-ready polling and UI dispatch.",
		"completion":            "RunBackgroundTurn invocation through completed voice delivery; includes inference, synthesis and playback, not host dispatch alone.",
		"stimulus_to_interrupt": "Independent stimulus player's start observation to the post-stop SpeakingInterrupted notification; not acoustic onset or detector timestamp.",
		"stimulus_to_stopped":   "Same post-stop SpeakingInterrupted notification as stimulus_to_interrupt; no separate stop timestamp is recorded.",
		"detection_to_stopped":  "Production interruption detection to playback-stop observation; use paired baseline regression for the 50ms allowance.",
	}}
	defer func() {
		if runErr != nil {
			summary.Status = "failed"
			summary.Error = runErr.Error()
			summary.Timings = nil
		}
		runErr = errors.Join(runErr, writeControlJSON(filepath.Join(o.Output, "summary.json"), summary))
	}()
	for i := 0; i < o.Samples+o.Warmups; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		sample := controlPopulationSample{Index: i + 1, Warmup: i < o.Warmups, Started: time.Now().UTC()}
		sampleDir := filepath.Join(o.Output, fmt.Sprintf("sample-%03d", i+1))
		if err := os.Mkdir(sampleDir, 0700); err != nil {
			return err
		}
		args := []string{"-scenario", o.Scenario, "-fixture-config", o.Config, "-stimulus-device", o.Stimulus, "-runs-dir", filepath.Join(sampleDir, "runs")}
		sample.Command = append([]string{o.Binary}, args...)
		err := verifyControlInputs(inputs)
		if err == nil {
			sampleCtx, cancel := context.WithTimeout(ctx, o.Timeout)
			err = child(sampleCtx, o.Binary, args, filepath.Join(sampleDir, "child.log"))
			cancel()
		}
		if err == nil {
			err = verifyControlInputs(inputs)
		}
		if err == nil {
			err = readControlSample(filepath.Join(sampleDir, "runs"), scn, &sample)
		}
		if err == nil && len(summary.Samples) > 0 {
			err = compareControlIdentity(summary.Samples[0], sample)
		}
		sample.Finished = time.Now().UTC()
		if err != nil {
			sample.Error = err.Error()
		}
		summary.Samples = append(summary.Samples, sample)
		if writeErr := writeControlJSON(filepath.Join(sampleDir, "sample.json"), sample); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		if err != nil {
			return fmt.Errorf("sample %d failed (retained, not replaced): %w", i+1, err)
		}
		if !sample.Warmup {
			summary.Measured++
		}
		if err := writeControlJSON(filepath.Join(o.Output, "progress.json"), summary); err != nil {
			return err
		}
		fmt.Printf("control population: %d/%d complete (warmup=%t)\n", i+1, o.Samples+o.Warmups, sample.Warmup)
	}
	summary.Timings = make(map[string]controlTimingSummary)
	values := make(map[string][]time.Duration)
	for _, s := range summary.Samples {
		if !s.Warmup {
			for k, v := range s.Timings {
				values[k] = append(values[k], v)
			}
		}
	}
	for k, v := range values {
		if len(v) != o.Samples {
			return fmt.Errorf("incomplete timing %s: %d/%d", k, len(v), o.Samples)
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		summary.Timings[k] = controlTimingSummary{len(v), percentile(v, .5), percentile(v, .95)}
	}
	summary.Status = "completed"
	return nil
}

func executeControlChild(ctx context.Context, binary string, args []string, logPath string) error {
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	// Allow normal capture/player teardown on cancellation, then bound a stuck child.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	return errors.Join(err, ctx.Err(), log.Close())
}

func controlFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
func verifyControlInputs(inputs map[string]string) error {
	for path, want := range inputs {
		got, err := controlFileHash(path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("pinned input changed: %s", path)
		}
	}
	return nil
}
func writeControlJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replacement preserves the last complete progress record on a crash.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func readControlJSON(path string, v any, hashes map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	hashes[path] = fmt.Sprintf("%x", sha256.Sum256(data))
	return nil
}
func readControlSample(root string, scn *Scenario, sample *controlPopulationSample) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return errors.New("sample must have exactly one retained run")
	}
	run := filepath.Join(root, entries[0].Name())
	sample.Run = run
	sample.Artifacts = make(map[string]string)
	var m metricsFile
	if err := readControlJSON(filepath.Join(run, "metrics.json"), &m, sample.Artifacts); err != nil {
		return err
	}
	if m.CrackleFailingWavs != nil && *m.CrackleFailingWavs != 0 {
		return errors.New("playback quality score failed")
	}
	if m.StopReason != "completed" || m.Scenario != scn.Name || m.Turns != 1 {
		return errors.New("child metrics did not record the complete expected control")
	}
	profilePath := filepath.Join(run, "config.resolved.yaml")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, &sample.Profile); err != nil {
		return err
	}
	sample.Artifacts[profilePath] = fmt.Sprintf("%x", sha256.Sum256(data))
	if sample.Profile.TTSProvider == "" || sample.Profile.silent() || sample.Profile.LiveProvider == "" {
		return errors.New("missing live/voice profile")
	}
	voice, err := readControlVoice(filepath.Join(run, "events.jsonl"), sample.Profile.TTSProvider, scn.Turns[0].Op, sample.Artifacts)
	if err != nil {
		return err
	}
	sample.Voice = voice
	turn := scn.Turns[0]
	dir := filepath.Join(run, "acoustic", turn.ID)
	switch turn.Op {
	case "acoustic_interrupt":
		var r interruptionReport
		if err := readControlJSON(filepath.Join(dir, "interruption.json"), &r, sample.Artifacts); err != nil {
			return err
		}
		if r.Error != "" || r.StimulusError != "" || !r.StimulusCompleted || !r.ProductionInterrupted || r.InitialTranscript == "" || r.Expected != turn.ExpectedTranscript || normalizeAcousticText(r.Transcript) != normalizeAcousticText(turn.ExpectedTranscript) {
			return errors.New("interruption control did not retain the expected speech and production interruption")
		}
		initialExpected, err := readAcousticMetadata(strings.TrimSuffix(turn.WAV, filepath.Ext(turn.WAV)) + ".json")
		if err != nil {
			return err
		}
		if normalizeAcousticText(r.InitialTranscript) != normalizeAcousticText(initialExpected) {
			return errors.New("interruption initial transcript does not match pinned fixture metadata")
		}
		if r.ResponseStarted.IsZero() || r.StimulusStarted.IsZero() || !r.StimulusEnded.After(r.StimulusStarted) || r.InterruptedAfter <= 0 || r.PlaybackStoppedAfter < r.InterruptedAfter || r.DetectionToStop <= 0 || r.PlaybackStoppedAfter != r.InterruptedAfter {
			return errors.New("invalid interruption timing ordering")
		}
		sample.Timings = map[string]time.Duration{"stimulus_to_interrupt": r.InterruptedAfter, "stimulus_to_stopped": r.PlaybackStoppedAfter, "detection_to_stopped": r.DetectionToStop}
		sample.Devices = []audio.NegotiatedDevice{r.NegotiatedCapture, r.NegotiatedResponse, r.NegotiatedStimulus}
	case "acoustic_completion":
		var r completionReport
		if err := readControlJSON(filepath.Join(dir, "completion.json"), &r, sample.Artifacts); err != nil {
			return err
		}
		if r.Error != "" || !r.Acknowledged || !r.Yielded || !r.Communicated || r.InitialTranscript != "" || r.Expected != turn.ExpectedTranscript || normalizeAcousticText(r.Transcript) != normalizeAcousticText(turn.ExpectedTranscript) {
			return errors.New("completion control did not acknowledge, yield, deliver and retain expected speech")
		}
		if math.IsNaN(r.PauseMS) || math.IsInf(r.PauseMS, 0) || r.PauseMS <= 0 || r.PauseMS > 300 || math.IsNaN(r.CompletionMS) || math.IsInf(r.CompletionMS, 0) || r.CompletionMS <= 0 || r.CompletionMS > float64(time.Hour/time.Millisecond) {
			return errors.New("invalid completion timing")
		}
		sample.Timings = map[string]time.Duration{"pause": time.Duration(r.PauseMS * float64(time.Millisecond)), "completion": time.Duration(r.CompletionMS * float64(time.Millisecond))}
		sample.Devices = []audio.NegotiatedDevice{r.NegotiatedCapture, r.NegotiatedResponse, r.NegotiatedStimulus}
	}
	for _, d := range sample.Devices {
		if !d.IdentityKnown || d.Name == "" || d.SampleRate <= 0 || d.Channels <= 0 || d.ClientSampleRate <= 0 || d.ClientChannels <= 0 {
			return errors.New("missing negotiated physical device identity/format")
		}
	}
	return nil
}
func compareControlIdentity(a, b controlPopulationSample) error {
	// Each child has a deliberately different work/artifact directory.
	a.Profile.WorkDir = ""
	b.Profile.WorkDir = ""
	if a.Voice != b.Voice {
		return errors.New("actual synthesizer changed within population")
	}
	if a.Profile != b.Profile {
		return errors.New("resolved profile changed within population")
	}
	if len(a.Devices) != len(b.Devices) {
		return errors.New("device count changed within population")
	}
	for i, d := range a.Devices {
		if d != b.Devices[i] {
			return errors.New("negotiated device changed within population")
		}
	}
	return nil
}

// Provider configuration alone cannot exclude degraded fallback audio. Keep the
// production terminal metrics as the actual engine identity for this population.
func readControlVoice(path, configured, operation string, hashes map[string]string) (controlVoiceIdentity, error) {
	var voice controlVoiceIdentity
	data, err := os.ReadFile(path)
	if err != nil {
		return voice, err
	}
	hashes[path] = fmt.Sprintf("%x", sha256.Sum256(data))
	dec := json.NewDecoder(bytes.NewReader(data))
	voiced := 0
	for {
		var line struct {
			Type  string          `json:"type"`
			Event json.RawMessage `json:"event"`
			Error string          `json:"error"`
		}
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return voice, fmt.Errorf("invalid event log: %w", err)
		}
		if line.Error != "" || line.Type == "Error" {
			return voice, errors.New("event log contains a public or serialization error")
		}
		if line.Type != "TurnMetrics" {
			continue
		}
		var m events.TurnMetrics
		if err := json.Unmarshal(line.Event, &m); err != nil {
			return voice, err
		}
		if m.Degraded || m.TTSFallbacks != 0 || m.StallWatchdogFired {
			return voice, errors.New("degraded/fallback/stalled synthesis cannot enter control population")
		}
		if m.AudibleSink != "local_speaker" {
			if operation != "acoustic_completion" || m.Outcome != "yielded_for_completion" || m.Interrupted {
				return voice, errors.New("unexpected nonvoiced terminal outcome in control")
			}
			continue
		}
		if m.TTSProvider == "" || m.TTSProvider != configured || m.PlaybackStartElapsed <= 0 || (m.Outcome != "completed" && m.Outcome != "interrupted") {
			return voice, errors.New("actual voice engine/playback outcome does not match configured physical control")
		}
		wantInterrupted := operation == "acoustic_interrupt" && voiced == 0
		if m.Interrupted != wantInterrupted || (m.Outcome == "interrupted") != wantInterrupted {
			return voice, errors.New("terminal voice outcome disagrees with physical control phase")
		}
		actual := controlVoiceIdentity{m.TTSProvider, m.TTSTier, m.TTSPlace}
		if voiced > 0 && actual != voice {
			return voice, errors.New("actual synthesizer changed within control")
		}
		voice = actual
		voiced++
	}
	if voiced != 2 {
		return voice, fmt.Errorf("expected two physical voiced turns, got %d", voiced)
	}
	return voice, nil
}
