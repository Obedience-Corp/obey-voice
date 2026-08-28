package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installAudio8Export writes the two files Audio8ExportInstalled looks for.
func installAudio8Export(t *testing.T, modelsDir string) string {
	t.Helper()
	dir := filepath.Join(modelsDir, Audio8Subdir)
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{Audio8TokenizerPath(dir), filepath.Join(dir, "runtime_manifest.json")} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// audio8 is a provider obey-voice does not manage assets for, so before this
// it fell through to "not a managed provider" and told the user to switch away
// from a provider that works. Each row is one tts-provider verdict.
func TestDiagnoseTTSProviderAudio8(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		install    bool
		wantSev    Severity
		wantDetail string
		wantRemedy []string
		denyRemedy []string
		wantNoQwen bool
	}{
		{
			name: "installed", provider: "audio8", install: true,
			wantSev: SeverityOK, wantDetail: "audio8", wantNoQwen: true,
		},
		{
			name: "selected but missing", provider: "audio8", install: false,
			wantSev: SeverityError, wantDetail: "no export is installed",
			// It must say how to install it, and must never suggest
			// abandoning the provider or running a command that installs
			// nothing for it (`models ensure` skips unmanaged providers).
			wantRemedy: []string{"install the Audio8 ONNX export"},
			denyRemedy: []string{"models ensure", "tts_provider=kokoro", "tts_provider=qwen3-tts"},
			wantNoQwen: true,
		},
		{
			name: "unknown provider still falls through", provider: "edge", install: false,
			wantSev: SeverityWarn, wantDetail: "is not a known provider",
			wantRemedy: []string{"tts_provider=kokoro", "tts_provider=qwen3-tts", "tts_provider=audio8"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelsDir := t.TempDir()
			var dir string
			if tc.install {
				dir = installAudio8Export(t, modelsDir)
			}
			cfg := &Config{
				BrainProvider: "ollama", OllamaModel: "test-model",
				OllamaHost:  "http://localhost:11434",
				STTProvider: "sherpa", TTSProvider: tc.provider,
			}
			diags := diagByName(Diagnose(cfg, modelsDir, failLookPath))
			d, ok := diags["tts-provider"]
			if !ok {
				t.Fatalf("no tts-provider diagnostic in %v", diags)
			}
			if d.Severity != tc.wantSev {
				t.Fatalf("tts-provider = %+v, want severity %s", d, tc.wantSev)
			}
			if !strings.Contains(d.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", d.Detail, tc.wantDetail)
			}
			if tc.install && !strings.Contains(d.Detail, dir) {
				t.Errorf("detail = %q, want it to name the export dir %q", d.Detail, dir)
			}
			for _, want := range tc.wantRemedy {
				if !strings.Contains(d.Remediation, want) {
					t.Errorf("remediation = %q, want it to contain %q", d.Remediation, want)
				}
			}
			for _, deny := range tc.denyRemedy {
				if strings.Contains(d.Remediation, deny) {
					t.Errorf("remediation = %q, must not suggest %q", d.Remediation, deny)
				}
			}
			if tc.wantSev == SeverityWarn && d.Remediation == "" {
				t.Error("a warn row without a remediation is not actionable")
			}
			if tc.wantNoQwen {
				for name := range diags {
					if strings.HasPrefix(name, "qwen3-tts-") {
						t.Errorf("audio8 diagnosis probed the optional Qwen check %q", name)
					}
				}
			}
		})
	}
}

func TestAudio8ExportInstalled(t *testing.T) {
	if Audio8ExportInstalled("") {
		t.Error(`Audio8ExportInstalled("") = true, want false`)
	}
	modelsDir := t.TempDir()
	dir := filepath.Join(modelsDir, Audio8Subdir)
	if Audio8ExportInstalled(dir) {
		t.Error("Audio8ExportInstalled() = true for a missing dir, want false")
	}
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Audio8TokenizerPath(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Audio8ExportInstalled(dir) {
		t.Error("Audio8ExportInstalled() = true without runtime_manifest.json, want false")
	}
	installAudio8Export(t, modelsDir)
	if !Audio8ExportInstalled(dir) {
		t.Error("Audio8ExportInstalled() = false for a complete export, want true")
	}
}
