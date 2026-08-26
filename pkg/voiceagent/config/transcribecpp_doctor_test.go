package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installTranscribeCPPGGUF writes the managed GGUF into a fresh models dir.
func installTranscribeCPPGGUF(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	asset, err := TranscribeCPPModel("")
	if err != nil {
		t.Fatal(err)
	}
	path := asset.ModelPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeDeviceCLI writes a transcribe-cli stand-in whose --list-devices prints
// devices, and returns a lookPath that resolves any name to it.
func fakeDeviceCLI(t *testing.T, devices string) func(string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcribe-cli")
	script := "#!/bin/sh\ncat <<'DEVICES'\n" + devices + "DEVICES\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func(string) (string, error) { return path, nil }
}

const (
	gpuDeviceList = `2 compute device(s):
  [0] Apple M4 Max
      name=MTL0  kind=metal  type=gpu  id=(none)
  [1] Apple M4 Max
      name=CPU  kind=cpu  type=cpu  id=(none)
`
	cpuOnlyDeviceList = `1 compute device(s):
  [0] Generic
      name=CPU  kind=cpu  type=cpu  id=(none)
`
)

func TestDiagnoseTranscribeCPPBinary(t *testing.T) {
	installed := installTranscribeCPPGGUF(t)
	gpuLookPath := fakeDeviceCLI(t, gpuDeviceList)
	cpuLookPath := fakeDeviceCLI(t, cpuOnlyDeviceList)

	tests := []struct {
		name         string
		modelsDir    string
		lookPath     func(string) (string, error)
		model        string
		wantSeverity Severity
		wantDetail   string
	}{
		{
			name: "missing binary", modelsDir: installed, lookPath: failLookPath,
			wantSeverity: SeverityError, wantDetail: "not found in PATH",
		},
		{
			name: "missing gguf", modelsDir: t.TempDir(), lookPath: okLookPath,
			wantSeverity: SeverityError, wantDetail: "GGUF is missing",
		},
		{
			name: "unknown model", modelsDir: installed, lookPath: okLookPath, model: "bogus",
			wantSeverity: SeverityError, wantDetail: "unsupported transcribe.cpp model",
		},
		{
			// Everything installed, but the machine has no GPU lane: --backend
			// auto would run on CPU, slower than the default provider.
			name: "cpu-only machine", modelsDir: installed, lookPath: cpuLookPath,
			wantSeverity: SeverityError, wantDetail: "no GPU lane",
		},
		{
			name: "binary cannot list devices", modelsDir: installed, lookPath: okLookPath,
			wantSeverity: SeverityError, wantDetail: "--list-devices",
		},
		{
			name: "installed", modelsDir: installed, lookPath: gpuLookPath,
			wantSeverity: SeverityOK, wantDetail: "backends: metal, cpu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				STTProvider:         "transcribecpp",
				TranscribeCPPBinary: "transcribe-cli",
				TranscribeCPPModel:  tt.model,
				TTSProvider:         "kokoro",
			}
			d := diagByName(Diagnose(cfg, tt.modelsDir, tt.lookPath))["transcribecpp-binary"]
			if d.Severity != tt.wantSeverity {
				t.Fatalf("transcribecpp-binary = %+v, want severity %s", d, tt.wantSeverity)
			}
			if !strings.Contains(d.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", d.Detail, tt.wantDetail)
			}
			if tt.wantSeverity == SeverityError && d.Remediation == "" {
				t.Error("an error diagnostic must carry remediation")
			}
		})
	}
}

func TestDiagnoseDefaultProviderNeverMentionsTranscribeCPP(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa-parakeet", TTSProvider: "kokoro"}
	for _, d := range Diagnose(cfg, t.TempDir(), failLookPath) {
		if strings.Contains(d.Name, "transcribecpp") || strings.Contains(d.Detail, "transcribe.cpp") {
			t.Errorf("sherpa-parakeet doctor mentions the optional provider: %+v", d)
		}
	}
}
