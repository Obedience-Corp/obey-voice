package config

import (
	"strings"
	"testing"
)

func TestQwenTTSWorkersOfClampsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int
		want  int
	}{
		{"unset (zero value)", 0, DefaultQwenTTSWorkers},
		{"negative", -1, DefaultQwenTTSWorkers},
		{"above max", MaxQwenTTSWorkers + 1, DefaultQwenTTSWorkers},
		{"at min", MinQwenTTSWorkers, MinQwenTTSWorkers},
		{"at max", MaxQwenTTSWorkers, MaxQwenTTSWorkers},
		{"in range", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QwenTTSWorkersOf(&Config{QwenTTSWorkers: tc.value}); got != tc.want {
				t.Errorf("QwenTTSWorkersOf(%d) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
	if got := QwenTTSWorkersOf(nil); got != DefaultQwenTTSWorkers {
		t.Errorf("QwenTTSWorkersOf(nil) = %d, want %d", got, DefaultQwenTTSWorkers)
	}
}

func TestQwenTTSWorkersSchemaRoundTrip(t *testing.T) {
	spec, ok := SpecFor("qwen_tts_workers")
	if !ok {
		t.Fatal("qwen_tts_workers has no KeySpec")
	}
	if spec.Type != TypeInt || spec.Default != DefaultQwenTTSWorkers {
		t.Fatalf("spec type=%v default=%v, want int defaulting to %d", spec.Type, spec.Default, DefaultQwenTTSWorkers)
	}
	if spec.Min == nil || *spec.Min != float64(MinQwenTTSWorkers) || spec.Max == nil || *spec.Max != float64(MaxQwenTTSWorkers) {
		t.Fatalf("spec min=%v max=%v, want %d-%d so a front end cannot offer a count no pool runs", spec.Min, spec.Max, MinQwenTTSWorkers, MaxQwenTTSWorkers)
	}
}

// The row is the only place the cost of a second worker is visible before it
// is paid, so it has to say the count, where it runs, and when it is ignored.
func TestDiagnoseQwenWorkers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cfg          *Config
		wantSeverity Severity
		wantDetail   string
	}{
		{
			name:         "another provider has no row",
			cfg:          &Config{TTSProvider: "kokoro"},
			wantSeverity: SeverityOK,
		},
		{
			name:         "default is one worker",
			cfg:          &Config{TTSProvider: "qwen3-tts"},
			wantSeverity: SeverityOK,
			wantDetail:   "1 worker",
		},
		{
			name:         "two workers name the experiment",
			cfg:          &Config{TTSProvider: "qwen3-tts", QwenTTSWorkers: 2},
			wantSeverity: SeverityOK,
			wantDetail:   "2 workers",
		},
		{
			name:         "lan ignores the count",
			cfg:          &Config{TTSProvider: "qwen3-tts", QwenTTSWorkers: 2, TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://speech:7263", TTSWorkerToken: "t"},
			wantSeverity: SeverityOK,
			wantDetail:   "ignored",
		},
		{
			name:         "out of range is clamped and said so",
			cfg:          &Config{TTSProvider: "qwen3-tts", QwenTTSWorkers: 9},
			wantSeverity: SeverityWarn,
			wantDetail:   "outside 1-4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := DiagnoseQwenWorkers(tc.cfg, t.TempDir())
			if tc.wantDetail == "" {
				if len(diags) != 0 {
					t.Fatalf("got %d diagnostics, want none", len(diags))
				}
				return
			}
			if len(diags) != 1 {
				t.Fatalf("got %d diagnostics, want 1", len(diags))
			}
			if diags[0].Severity != tc.wantSeverity {
				t.Errorf("severity = %v, want %v", diags[0].Severity, tc.wantSeverity)
			}
			if !strings.Contains(diags[0].Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", diags[0].Detail, tc.wantDetail)
			}
		})
	}
}

// A second worker is unmeasured, so the row must point at the gate rather than
// reading as a tuning knob.
func TestDiagnoseQwenWorkersPointsTwoWorkersAtTheBenchmark(t *testing.T) {
	diags := DiagnoseQwenWorkers(&Config{TTSProvider: "qwen3-tts", QwenTTSWorkers: 2}, t.TempDir())
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	if !strings.Contains(diags[0].Remediation, "docs/benchmarks") {
		t.Errorf("remediation = %q, want it to name the benchmark that gates more than one worker", diags[0].Remediation)
	}
}
