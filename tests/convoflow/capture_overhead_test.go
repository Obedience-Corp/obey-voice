package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureStatsNearestRankAndPairedNegativeValues(t *testing.T) {
	values := []float64{20, -5, 10, 0, 5}
	got := captureStats(values)
	if got.N != 5 || got.MinMS != -5 || got.MaxMS != 20 || got.MedianMS != 5 || got.P95MS != 20 || got.MeanMS != 6 {
		t.Fatalf("unexpected distribution: %+v", got)
	}
	if math.Abs(got.SDMS-math.Sqrt(92.5)) > 1e-9 {
		t.Fatalf("sample SD=%g", got.SDMS)
	}
	if values[0] != 20 {
		t.Fatal("statistics mutated pair order")
	}
	if captureQuantile([]float64{3, 1, 2}, .5) != 2 {
		t.Fatal("nearest-rank median must sort its own input")
	}
}

func captureAnalysisFixture(t *testing.T, mutate func([]map[string]any) []map[string]any) string {
	t.Helper()
	root := t.TempDir()
	entries := []captureArm{{Warmup: true}, {Warmup: true, Capture: true}}
	for pair := 1; pair <= 30; pair++ {
		entries = append(entries, captureArm{Pair: pair}, captureArm{Pair: pair, Capture: true})
	}
	write := func(name string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("population-plan.json", map[string]any{"seed": 7007, "entries": entries})
	write("provider-provenance.json", map[string]any{"binary_sha256": "test", "fixture_config_sha256": "test", "wav_sha256": "test", "brain_model": "test", "stt_model": "test", "tts_provider": "test", "source_revision": "test", "silent_output": true, "requested_output_device": "test-virtual", "playback_gain": 0, "work_enabled": false})
	rows := []map[string]any{}
	for i, arm := range entries {
		delay := 100.
		if arm.Capture {
			delay = 105
		}
		id := fmt.Sprintf("id-%d", i)
		rows = append(rows, map[string]any{"type": "turn", "population": arm, "population_valid": true, "quiet_output_verified": true, "output_device": map[string]any{"name": "test-virtual", "requested_name": "test-virtual", "identity_known": true}, "playback_gain": 0, "capture": arm.Capture, "metrics_available": true, "outcome": "completed", "audible_sink": "local_speaker", "timing": map[string]any{"origin": "finalized_input", "raw_turn_start": map[string]any{"finalized_input_ms": 20, "playback_start_ms": 20 + delay}, "normalized": map[string]any{"playback_start_ms": delay}}, "trace": map[string]any{"turn_id": id, "response_id": id, "runtime_id": "runtime", "session_id": "session", "conversation_epoch": "epoch"}, "envelope": map[string]any{"response_id": id, "prompt_hash": "pinned"}, "transcript": "same input"})
	}
	if mutate != nil {
		rows = mutate(rows)
	}
	f, err := os.Create(filepath.Join(root, "observations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestCaptureAnalysisRequiresCompleteMatchedQuietPopulation(t *testing.T) {
	root := captureAnalysisFixture(t, nil)
	report, err := analyzeCaptureOverhead(root)
	if err != nil || report.Decision != "pass" || report.P95RegressionMS != 5 || report.CI95[1] != 5 {
		t.Fatalf("matched constant population: %+v %v", report, err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func([]map[string]any) []map[string]any
	}{
		{"incomplete", "incomplete", func(rows []map[string]any) []map[string]any { return rows[:len(rows)-1] }},
		{"failed", "no silent resampling", func(rows []map[string]any) []map[string]any { rows[4]["population_valid"] = false; return rows }},
		{"mixed_prompt", "prompt/transcript changed", func(rows []map[string]any) []map[string]any { rows[4]["transcript"] = "different input"; return rows }},
		{"audible", "no silent resampling", func(rows []map[string]any) []map[string]any { rows[4]["playback_gain"] = 1; return rows }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := captureAnalysisFixture(t, tc.mutate)
			report, err := analyzeCaptureOverhead(root)
			if err == nil || report.Decision == "pass" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid population accepted: %+v %v", report, err)
			}
		})
	}
}
