package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestPrepareAcousticSuiteRequiresRecordedMetadataAndGeneratesScenarios(t *testing.T) {
	fixtures, out := t.TempDir(), t.TempDir()
	for i := 1; i <= 3; i++ {
		name := filepath.Join(fixtures, "synth-0"+string(rune('0'+i))+".wav")
		if err := audio.WriteWAVFloat32(name, audio.SampleRate, []float32{0.1, 0.2, 0.1}); err != nil {
			t.Fatal(err)
		}
		meta, _ := json.Marshal(map[string]string{"text": "known phrase " + string(rune('0'+i))})
		if err := os.WriteFile(filepath.Join(fixtures, "synth-0"+string(rune('0'+i))+".json"), append(meta, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := audio.WriteWAVFloat32(filepath.Join(fixtures, "counting.wav"), audio.SampleRate, []float32{0.1, 0.2, 0.1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtures, "counting.json"), []byte(`{"text":"counting phrase"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAcousticSuite(out, fixtures); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "baseline-33.yaml", "worker-33.yaml", "completion.yaml", "interruption.yaml"} {
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
