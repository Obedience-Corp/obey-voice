package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	for _, name := range []string{"manifest.json", "baseline-33.yaml", "worker-33.yaml", "completion.yaml", "response.yaml", "interruption.yaml", "startup-full.yaml"} {
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
