//go:build !integration

package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestApplyBenchmarkTTSPlace(t *testing.T) {
	tests := []struct {
		name    string
		place   string
		cfg     config.Config
		want    string
		wantErr string
	}{
		{
			// The flag is an override, not a requirement: an unset value must
			// leave every existing invocation measuring what config says.
			name:  "empty leaves the configured place",
			place: "",
			cfg:   config.Config{TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
			want:  config.TTSPlaceLAN,
		},
		{
			name:  "local",
			place: config.TTSPlaceLocal,
			cfg:   config.Config{TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
			want:  config.TTSPlaceLocal,
		},
		{
			name:  "lan with a usable worker",
			place: config.TTSPlaceLAN,
			cfg:   config.Config{TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
			want:  config.TTSPlaceLAN,
		},
		{
			name:  "case and spacing are normalized",
			place: "  LAN ",
			cfg:   config.Config{TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
			want:  config.TTSPlaceLAN,
		},
		{
			// Failing here rather than at the first synthesis is the point: a
			// run that falls back to Kokoro would write a Kokoro measurement
			// under a lan label.
			name:    "lan without a worker url",
			place:   config.TTSPlaceLAN,
			cfg:     config.Config{},
			wantErr: "tts_worker_url",
		},
		{
			name:    "plaintext off-box",
			place:   config.TTSPlaceLAN,
			cfg:     config.Config{TTSWorkerURL: "http://192.168.1.10:7263", TTSWorkerToken: "t"},
			wantErr: "plaintext",
		},
		{
			name:    "unknown place",
			place:   "cloud",
			cfg:     config.Config{},
			wantErr: "unsupported tts_place",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			err := applyBenchmarkTTSPlace(&cfg, tt.place)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("applyBenchmarkTTSPlace(%q) err = %v, want it to mention %q", tt.place, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyBenchmarkTTSPlace(%q): %v", tt.place, err)
			}
			if cfg.TTSPlace != tt.want {
				t.Errorf("TTSPlace = %q, want %q", cfg.TTSPlace, tt.want)
			}
		})
	}
}

// A benchmark artifact is only comparable across topologies if every run
// carries its own. The place and the worker's streaming frame ride on the
// per-run metrics rather than the envelope because a sweep can measure
// several engines in one file (E9).
func TestBenchmarkJSONCarriesSpeechTopology(t *testing.T) {
	result := benchmarkResult{
		Mode:      "full-turn",
		Iteration: 1,
		Metrics: events.TurnMetrics{
			PlaybackStartElapsed: 1500 * time.Millisecond,
			TTSProvider:          "qwen3-tts",
			TTSTier:              "1.7b",
			TTSPlace:             "lan",
			TTSWorkerStreaming:   true,
		},
	}

	raw, err := json.Marshal([]benchmarkResult{result})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []struct {
		Metrics map[string]any `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("results = %d, want 1", len(decoded))
	}
	m := decoded[0].Metrics
	for key, want := range map[string]any{
		"TTSPlace":             "lan",
		"TTSTier":              "1.7b",
		"TTSProvider":          "qwen3-tts",
		"TTSWorkerStreaming":   true,
		"PlaybackStartElapsed": float64(1500 * time.Millisecond),
	} {
		got, ok := m[key]
		if !ok {
			t.Errorf("metrics.%s missing; scripts/bench.sh reads provenance straight off these keys", key)
			continue
		}
		if got != want {
			t.Errorf("metrics.%s = %v, want %v", key, got, want)
		}
	}
}
