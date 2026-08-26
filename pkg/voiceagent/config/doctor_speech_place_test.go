package config

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func speechPlaceRow(t *testing.T, cfg *Config, probe SpeechProbe) Diagnostic {
	t.Helper()
	diags := DiagnoseSpeechPlace(context.Background(), cfg, probe)
	if len(diags) != 1 || diags[0].Name != "speech-place" {
		t.Fatalf("diags=%+v, want one speech-place row", diags)
	}
	return diags[0]
}

func TestDiagnoseSpeechPlaceFailures(t *testing.T) {
	unreachable := func(context.Context, *Config) (SpeechProbeResult, error) {
		return SpeechProbeResult{}, errors.New("connection refused")
	}
	for _, tc := range []struct {
		name  string
		cfg   Config
		probe SpeechProbe
		want  Severity
		match string
	}{
		{
			name:  "unreachable warns so talk still starts",
			cfg:   Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
			probe: unreachable,
			want:  SeverityWarn,
			match: "did not answer",
		},
		{
			name:  "strict makes it an error",
			cfg:   Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t", TTSLanStrict: true},
			probe: unreachable,
			want:  SeverityError,
			match: "did not answer",
		},
		{
			name: "invalid url never dials",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://192.168.1.1:7263"},
			probe: func(context.Context, *Config) (SpeechProbeResult, error) {
				t.Fatal("must not dial")
				return SpeechProbeResult{}, nil
			},
			want:  SeverityError,
			match: "plaintext",
		},
		{
			name: "lan without a url never dials",
			cfg:  Config{TTSPlace: TTSPlaceLAN},
			probe: func(context.Context, *Config) (SpeechProbeResult, error) {
				t.Fatal("must not dial")
				return SpeechProbeResult{}, nil
			},
			want:  SeverityError,
			match: "tts_worker_url",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := speechPlaceRow(t, &tc.cfg, tc.probe)
			if row.Severity != tc.want {
				t.Fatalf("severity=%s want %s (%s)", row.Severity, tc.want, row.Detail)
			}
			if !strings.Contains(row.Detail, tc.match) {
				t.Fatalf("detail=%q want %q", row.Detail, tc.match)
			}
			if row.Remediation == "" {
				t.Fatal("a failing row must say what to do")
			}
		})
	}
}

func TestDiagnoseSpeechPlaceLocalNeverDials(t *testing.T) {
	for _, place := range []string{"", TTSPlaceLocal} {
		row := speechPlaceRow(t, &Config{TTSPlace: place}, func(context.Context, *Config) (SpeechProbeResult, error) {
			t.Fatal("local must not dial")
			return SpeechProbeResult{}, nil
		})
		if row.Severity != SeverityOK || !strings.Contains(row.Detail, "local") {
			t.Fatalf("row=%+v", row)
		}
	}
}

func TestDiagnoseSpeechPlaceReportsTheWorker(t *testing.T) {
	row := speechPlaceRow(t, &Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"},
		func(context.Context, *Config) (SpeechProbeResult, error) {
			return SpeechProbeResult{Host: "studio:7263", RTT: 3 * time.Millisecond, Streaming: false, Tier: "0.6b"}, nil
		})
	if row.Severity != SeverityOK {
		t.Fatalf("row=%+v", row)
	}
	for _, want := range []string{"studio:7263", "3ms", "streaming=false", "tier 0.6b"} {
		if !strings.Contains(row.Detail, want) {
			t.Fatalf("detail=%q missing %q", row.Detail, want)
		}
	}
}

// A LAN host owns no worker binary, no GGUF and no tier: those rows must not
// fail a machine that never needed them.
func TestDiagnoseSkipsLocalQwenAssetsOnLan(t *testing.T) {
	cfg := &Config{TTSProvider: "qwen3-tts", TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"}
	diags := Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", errors.New("not installed") })

	var provider *Diagnostic
	for i := range diags {
		if strings.HasPrefix(diags[i].Name, "qwen3-tts-binary") || strings.HasPrefix(diags[i].Name, "qwen3-tts-model") {
			t.Fatalf("lan host was asked for a local %s: %+v", diags[i].Name, diags[i])
		}
		if diags[i].Name == "tts-provider" {
			provider = &diags[i]
		}
	}
	if provider == nil || provider.Severity != SeverityOK || !strings.Contains(provider.Detail, "another computer") {
		t.Fatalf("tts-provider row=%+v", provider)
	}
}
