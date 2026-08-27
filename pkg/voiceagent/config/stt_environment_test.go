package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookPathFor(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, have := range installed {
			if have == name {
				return "/usr/local/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

func TestSTTEnvironmentGap(t *testing.T) {
	modelsDir := t.TempDir()
	withWeights := t.TempDir()
	asset, err := TranscribeCPPModel("")
	if err != nil {
		t.Fatal(err)
	}
	path := asset.ModelPath(withWeights)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		provider  string
		cfg       Config
		modelsDir string
		installed []string
		want      string
		wantFix   string
	}{
		{
			name: "transcribecpp binary missing", provider: STTProviderTranscribeCPP,
			modelsDir: withWeights,
			want:      "transcribe-cli is not on PATH", wantFix: "transcribecpp_binary",
		},
		{
			name: "transcribecpp custom binary is named", provider: STTProviderTranscribeCPP,
			cfg: Config{TranscribeCPPBinary: "/opt/transcribe"}, modelsDir: withWeights,
			want: "/opt/transcribe is not on PATH",
		},
		{
			name: "transcribecpp weights missing", provider: STTProviderTranscribeCPP,
			modelsDir: modelsDir, installed: []string{"transcribe-cli"},
			want: "weights are not downloaded", wantFix: "models ensure",
		},
		{
			name: "transcribecpp unknown model", provider: STTProviderTranscribeCPP,
			cfg: Config{TranscribeCPPModel: "nope"}, modelsDir: withWeights,
			installed: []string{"transcribe-cli"},
			want:      "nope", wantFix: "transcribecpp_model",
		},
		{
			name: "whispercpp binary missing", provider: STTProviderWhisperCPP,
			want: "whisper-cli is not on PATH", wantFix: "whispercpp_binary",
		},
		{
			name: "transcribecpp ready", provider: STTProviderTranscribeCPP,
			modelsDir: withWeights, installed: []string{"transcribe-cli"},
		},
		{
			name: "whispercpp ready", provider: STTProviderWhisperCPP,
			installed: []string{"whisper-cli"},
		},
		{name: "parakeet assets are ensured at start", provider: STTProviderParakeet},
		{name: "sherpa assets are ensured at start", provider: STTProviderSherpa},
		{name: "unknown provider", provider: "whisper-turbo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.modelsDir
			if dir == "" {
				dir = modelsDir
			}
			gap := STTEnvironmentGap(tc.provider, &tc.cfg, dir, lookPathFor(tc.installed...))
			if tc.want == "" {
				if gap.Missing() {
					t.Fatalf("gap = %+v, want none", gap)
				}
				return
			}
			if !strings.Contains(gap.Detail, tc.want) {
				t.Fatalf("detail = %q, want %q", gap.Detail, tc.want)
			}
			if tc.wantFix != "" && !strings.Contains(gap.Remediation, tc.wantFix) {
				t.Fatalf("remediation = %q, want %q", gap.Remediation, tc.wantFix)
			}
		})
	}
}

// The picker builds rows while rendering, so the gap check must not shell out.
func TestSTTEnvironmentGapToleratesNoLookPath(t *testing.T) {
	if gap := STTEnvironmentGap(STTProviderTranscribeCPP, &Config{}, t.TempDir(), nil); gap.Missing() {
		t.Fatalf("gap = %+v, want none without a resolver", gap)
	}
	if gap := STTEnvironmentGap(STTProviderTranscribeCPP, nil, t.TempDir(), lookPathFor()); gap.Missing() {
		t.Fatalf("gap = %+v, want none without a config", gap)
	}
}

// STTModes is what the picker writes as stt_mode; it must agree with what the
// normalizer accepts for that provider.
func TestSTTModesAgreeWithTheNormalizer(t *testing.T) {
	for _, provider := range []string{
		STTProviderSherpa, STTProviderParakeet, STTProviderWhisperCPP, STTProviderTranscribeCPP,
	} {
		modes := STTModes(provider)
		if len(modes) == 0 {
			t.Fatalf("%s has no modes", provider)
		}
		for _, mode := range modes {
			if _, err := NormalizeSTTWithMode(provider, mode); err != nil {
				t.Fatalf("%s/%s: %v", provider, mode, err)
			}
		}
	}
	if got := STTModes("whisper-turbo"); got != nil {
		t.Fatalf("unknown provider has modes: %v", got)
	}
	// The returned slice is a copy: a caller cannot edit the table.
	modes := STTModes(STTProviderSherpa)
	modes[0] = "mutated"
	if STTModes(STTProviderSherpa)[0] == "mutated" {
		t.Fatal("STTModes leaks the package table")
	}
}
