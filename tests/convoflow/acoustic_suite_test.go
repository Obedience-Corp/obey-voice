package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestPrepareAcousticSuiteRequiresRecordedMetadataAndGeneratesScenarios(t *testing.T) {
	fixtures, out := acousticSuiteTestFixtures(t), t.TempDir()
	if err := prepareAcousticSuite(out, fixtures); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "baseline-33.yaml", "worker-33.yaml", "completion.yaml", "completion-mute.yaml", "completion-speech.yaml", "response.yaml", "mute.yaml", "failure.yaml", "interruption.yaml", "startup-full.yaml"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(out, "baseline-33.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scn, err := loadScenarioString("generated", string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(scn.Turns) != 33 || !scn.Turns[0].LatencyWarmup || scn.Turns[3].LatencyWarmup {
		t.Fatalf("generated turns=%d warmup0=%t warmup3=%t", len(scn.Turns), scn.Turns[0].LatencyWarmup, scn.Turns[3].LatencyWarmup)
	}
	workerData, err := os.ReadFile(filepath.Join(out, "worker-33.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := loadScenarioString("worker", string(workerData))
	if err != nil {
		t.Fatal(err)
	}
	if !worker.Worker.SustainedLoad {
		t.Fatal("worker scenario did not require sustained load")
	}
	measured, starts, stops := 0, 0, 0
	for _, turn := range worker.Turns {
		switch turn.Op {
		case "start_latency_worker":
			starts++
		case "stop_latency_worker":
			stops++
		default:
			if turn.LatencyIteration > 0 {
				measured++
			}
		}
	}
	if measured != 33 || starts != 33 || stops != 33 {
		t.Fatalf("worker population measured=%d starts=%d stops=%d", measured, starts, stops)
	}
	completion, err := loadScenario(filepath.Join(out, "completion.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if completion.Turns[0].WAV != filepath.Join(fixtures, "synth-02.wav") || completion.Turns[0].ExpectedTranscript != "known phrase 2" {
		t.Fatalf("completion fixture and expected phrase differ: %+v", completion.Turns[0])
	}
	response, err := loadScenario(filepath.Join(out, "response.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	turn := response.Turns[0]
	if turn.Op != "acoustic_response" || turn.WAV != filepath.Join(fixtures, "counting.wav") || turn.ExpectedTranscript != "counting phrase" || turn.InterruptWAV != "" || turn.Duration.Duration != 3*time.Second {
		t.Fatalf("response control changed fixture or echo-only semantics: %+v", turn)
	}
	for _, name := range []string{"completion-mute", "completion-speech"} {
		data, err := os.ReadFile(filepath.Join(out, name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		control, err := loadScenarioString(name, string(data))
		if err != nil {
			t.Fatal(err)
		}
		want := "acoustic_" + strings.ReplaceAll(name, "-", "_")
		if len(control.Turns) != 1 || control.Turns[0].Op != want || control.Turns[0].WAV != filepath.Join(fixtures, "synth-02.wav") || control.Turns[0].ExpectedTranscript != "known phrase 2" {
			t.Fatalf("completion boundary changed fixture/op: %+v", control)
		}
	}
	for _, name := range []string{"mute", "failure"} {
		boundary, err := loadScenario(filepath.Join(out, name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(boundary.Turns) != 1 || boundary.Turns[0].Op != "acoustic_"+name || boundary.Turns[0].WAV != filepath.Join(fixtures, "synth-01.wav") || boundary.Turns[0].ExpectedTranscript != "known phrase 1" || boundary.Turns[0].Timeout.Duration != 180*time.Second {
			t.Fatalf("boundary fixture semantics changed: %+v", boundary)
		}
	}
	manifestData, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifestData), "counting.wav") {
		t.Fatal("manifest omitted counting fixture")
	}
}

func TestPrepareAcousticSuiteRejectsMissingFixtureMetadata(t *testing.T) {
	fixtures := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixtures, "synth-01.wav"), []byte("not wav"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAcousticSuite(t.TempDir(), fixtures); err == nil {
		t.Fatal("missing metadata accepted")
	}
}

func acousticSuiteTestFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		writeAcousticSuiteTestFixture(t, dir, fmt.Sprintf("synth-%02d", i), fmt.Sprintf("known phrase %d", i))
	}
	writeAcousticSuiteTestFixture(t, dir, "counting", "counting phrase")
	return dir
}

func writeAcousticSuiteTestFixture(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := audio.WriteWAVFloat32(filepath.Join(dir, name+".wav"), audio.SampleRate, []float32{0.1, 0.2, 0.1}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAcousticSuiteStartupFixtures(t *testing.T) {
	for _, withShort := range []bool{false, true} {
		t.Run(fmt.Sprintf("short=%t", withShort), func(t *testing.T) {
			fixtures, out := acousticSuiteTestFixtures(t), t.TempDir()
			if withShort {
				writeAcousticSuiteTestFixture(t, fixtures, "startup-short", "Wait.")
			} else {
				// Regeneration must not leave a stale optional control available to the recipe.
				if err := os.WriteFile(filepath.Join(out, "startup-short.yaml"), []byte("stale"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareAcousticSuite(out, fixtures); err != nil {
				t.Fatal(err)
			}
			names := []string{"startup-full"}
			if withShort {
				names = append(names, "startup-short")
			}
			for _, name := range names {
				scenario, err := loadScenario(filepath.Join(out, name+".yaml"))
				if err != nil {
					t.Fatal(err)
				}
				if len(scenario.Turns) != 1 {
					t.Fatalf("%s turns=%d", name, len(scenario.Turns))
				}
				turn := scenario.Turns[0]
				expectedWAV, expectedText := "synth-02.wav", "known phrase 2"
				if name == "startup-short" {
					expectedWAV, expectedText = "startup-short.wav", "Wait."
					if turn.StimulusCompleteWithin == nil || turn.StimulusCompleteWithin.Duration != 600*time.Millisecond {
						t.Fatal("startup-short missing observed completion deadline")
					}
				}
				if turn.Op != "acoustic_interrupt" || turn.WAV != filepath.Join(fixtures, "counting.wav") || turn.InterruptWAV != filepath.Join(fixtures, expectedWAV) || turn.ExpectedTranscript != expectedText {
					t.Fatalf("%s fixture mismatch: %+v", name, turn)
				}
				if turn.StimulusDelay == nil || turn.StimulusDelay.Duration != 0 {
					t.Fatalf("%s omitted explicit zero stimulus delay", name)
				}
			}
			if !withShort {
				if _, err := os.Stat(filepath.Join(out, "startup-short.yaml")); !os.IsNotExist(err) {
					t.Fatalf("optional scenario exists or unexpected error: %v", err)
				}
			}
			data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Fixtures []acousticFixture `json:"fixtures"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			expectedCount := 4
			if withShort {
				expectedCount++
			}
			if len(manifest.Fixtures) != expectedCount {
				t.Fatalf("manifest fixtures=%d, want %d", len(manifest.Fixtures), expectedCount)
			}
			for _, fixture := range manifest.Fixtures {
				wav, err := os.ReadFile(fixture.Path)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(wav)
				if fixture.SHA256 != hex.EncodeToString(hash[:]) {
					t.Fatalf("incorrect fingerprint for %s", fixture.Path)
				}
				if filepath.Base(fixture.Path) == "startup-short.wav" && fixture.Text != "Wait." {
					t.Fatalf("short text=%q", fixture.Text)
				}
			}
		})
	}
}

func TestPrepareAcousticSuiteRejectsInvalidStartupShort(t *testing.T) {
	for _, tc := range []struct {
		name, metadata              string
		invalidWAV, missingMetadata bool
	}{
		{name: "invalid WAV", metadata: `{"text":"Wait."}`, invalidWAV: true},
		{name: "missing metadata", missingMetadata: true},
		{name: "invalid metadata", metadata: "not json"},
		{name: "empty text", metadata: `{"text":" "}`},
		{name: "wrong phrase", metadata: `{"text":"Stop."}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtures := acousticSuiteTestFixtures(t)
			writeAcousticSuiteTestFixture(t, fixtures, "startup-short", "Wait.")
			meta := filepath.Join(fixtures, "startup-short.json")
			if tc.missingMetadata {
				if err := os.Remove(meta); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(meta, []byte(tc.metadata), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.invalidWAV {
				if err := os.WriteFile(filepath.Join(fixtures, "startup-short.wav"), []byte("not wav"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareAcousticSuite(t.TempDir(), fixtures); err == nil || !strings.Contains(err.Error(), "startup-short fixture") {
				t.Fatalf("invalid short fixture error=%v", err)
			}
		})
	}
}

func TestPrepareAcousticSuiteWorkerInterruptionUsesSameFixtures(t *testing.T) {
	fixtures, out := acousticSuiteTestFixtures(t), t.TempDir()
	if err := prepareAcousticSuite(out, fixtures); err != nil {
		t.Fatal(err)
	}
	baseline, err := loadScenario(filepath.Join(out, "interruption.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := loadScenario(filepath.Join(out, "interruption-worker.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if worker.LatencyCondition != "worker" || !worker.Worker.SustainedLoad || len(worker.Turns) != 3 {
		t.Fatalf("worker scenario=%+v", worker)
	}
	if worker.Turns[0].Op != "start_latency_worker" || worker.Turns[2].Op != "stop_latency_worker" {
		t.Fatal("worker lifetime not bounded")
	}
	a, b := baseline.Turns[0], worker.Turns[1]
	if b.Op != "acoustic_interrupt" || b.ID != a.ID || b.WAV != a.WAV || b.InterruptWAV != a.InterruptWAV || b.ExpectedTranscript != a.ExpectedTranscript || b.interruptionStimulusDelay() != a.interruptionStimulusDelay() {
		t.Fatalf("control inputs differ: baseline=%+v worker=%+v", a, b)
	}
	for _, turn := range worker.Turns {
		if turn.LatencyIteration != 0 {
			t.Fatal("control would enter ordinary reply collector")
		}
	}
}

func TestPrepareAcousticSuiteNearOnlyCalibration(t *testing.T) {
	fixtures, out := acousticSuiteTestFixtures(t), t.TempDir()
	writeAcousticSuiteTestFixture(t, fixtures, "startup-short", "Wait.")
	if err := prepareAcousticSuite(out, fixtures); err != nil {
		t.Fatal(err)
	}
	calibration, err := loadScenario(filepath.Join(out, "calibration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calibration.Turns) != 2 || calibration.LatencyCondition != "" || calibration.Worker.SustainedLoad || calibration.Input != "speech" || !calibration.Capture.Microphone || !calibration.Capture.Speakers {
		t.Fatalf("calibration is not two ordinary physical turns: %+v", calibration)
	}
	for i, want := range []struct{ name, text string }{{"synth-02.wav", "known phrase 2"}, {"startup-short.wav", "Wait."}} {
		turn := calibration.Turns[i]
		if turn.WAV != filepath.Join(fixtures, want.name) || turn.ExpectedTranscript != want.text || turn.Op != "" || turn.InterruptWAV != "" || turn.StimulusDelay != nil || turn.StimulusCompleteWithin != nil || turn.LatencyIteration != 0 || turn.LatencyWarmup || turn.Timeout.Duration != 120*time.Second {
			t.Fatalf("calibration turn %d changed fixture or admission semantics: %+v", i, turn)
		}
	}
	if calibration.Capture.STTDecodes {
		t.Fatal("default calibration unexpectedly records decoder inputs")
	}
	traced, err := loadScenario(filepath.Join(out, "calibration-decodes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !traced.Capture.STTDecodes || traced.LatencyCondition != "" || !traced.Capture.Microphone || !traced.Capture.Speakers || !reflect.DeepEqual(traced.Turns, calibration.Turns) {
		t.Fatalf("decoder variant changed fixture or ordinary-turn semantics: %+v", traced)
	}
	// A reused output directory must not advertise combined coverage after its
	// optional short fixture has been removed.
	if err := os.Remove(filepath.Join(fixtures, "startup-short.wav")); err != nil {
		t.Fatal(err)
	}
	if err := prepareAcousticSuite(out, fixtures); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"calibration.yaml", "calibration-decodes.yaml"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("combined %s survived missing short fixture: %v", name, err)
		}
	}
	if _, err := loadScenario(filepath.Join(out, "startup-full.yaml")); err != nil {
		t.Fatalf("full-only suite compatibility: %v", err)
	}
}
