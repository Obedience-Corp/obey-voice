package stt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Device listings shaped like the real transcribe-cli --list-devices output.
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

// fakeTranscribeCLI writes a transcribe-cli stand-in that answers
// --list-devices with devices, records its arguments, and writes text to the -o
// path, the way the real CLI does.
func fakeTranscribeCLI(t *testing.T, dir, text, devices string) string {
	t.Helper()
	path := filepath.Join(dir, "transcribe-cli")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--list-devices" ]; then
  cat <<'DEVICES'
` + devices + `DEVICES
  exit 0
fi
out=""
log=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -m) log="$log model=$2"; shift 2 ;;
    --backend) log="$log backend=$2"; shift 2 ;;
    -q) log="$log quiet"; shift ;;
    *) log="$log wav=$1"; shift ;;
  esac
done
printf "%s" "$log" > "` + dir + `/args.log"
printf "` + text + `\n" > "$out"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// transcribeCPPInstall lays out a models dir holding the managed GGUF and
// returns a config pointing at it plus the fake CLI's temp dir.
func transcribeCPPInstall(t *testing.T, text string) (*config.Config, string) {
	t.Helper()
	return transcribeCPPInstallOn(t, text, gpuDeviceList)
}

func transcribeCPPInstallOn(t *testing.T, text, devices string) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	asset, err := config.TranscribeCPPModel("")
	if err != nil {
		t.Fatal(err)
	}
	modelPath := asset.ModelPath(dir)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		ModelsDir:           dir,
		TranscribeCPPBinary: fakeTranscribeCLI(t, dir, text, devices),
	}, dir
}

func TestNewTranscribeCPPSTTFailsClosed(t *testing.T) {
	installed, dir := transcribeCPPInstall(t, "ignored")

	cpuOnly, _ := transcribeCPPInstallOn(t, "ignored", cpuOnlyDeviceList)
	brokenDir := t.TempDir()
	broken := filepath.Join(brokenDir, "transcribe-cli")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		mutate  func(cfg *config.Config)
		wantMsg string
	}{
		{
			// --backend auto would silently pick CPU here, where transcribe.cpp
			// is slower than the default provider it was chosen over.
			name:    "cpu-only machine",
			mutate:  func(cfg *config.Config) { *cfg = *cpuOnly },
			wantMsg: "no GPU lane",
		},
		{
			name:    "cpu-only machine names the remedy",
			mutate:  func(cfg *config.Config) { *cfg = *cpuOnly },
			wantMsg: "sherpa-parakeet",
		},
		{
			name:    "unlistable devices",
			mutate:  func(cfg *config.Config) { cfg.TranscribeCPPBinary = broken },
			wantMsg: "--list-devices",
		},
		{
			name:    "missing binary",
			mutate:  func(cfg *config.Config) { cfg.TranscribeCPPBinary = filepath.Join(dir, "absent-cli") },
			wantMsg: "transcribecpp_binary",
		},
		{
			name:    "unknown model",
			mutate:  func(cfg *config.Config) { cfg.TranscribeCPPModel = "bogus" },
			wantMsg: "unsupported transcribe.cpp model",
		},
		{
			name:    "missing gguf",
			mutate:  func(cfg *config.Config) { cfg.ModelsDir = t.TempDir() },
			wantMsg: "obey-voice models ensure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := *installed
			tt.mutate(&cfg)
			_, err := NewTranscribeCPPSTT(&cfg, nil, nil)
			if err == nil {
				t.Fatal("NewTranscribeCPPSTT() error = nil, want construction failure")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestTranscribeCPPTranscribeReadsTheOutputFile(t *testing.T) {
	cfg, dir := transcribeCPPInstall(t, "HELLO OBEY VOICE")

	provider, err := NewTranscribeCPPSTT(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewTranscribeCPPSTT() error = %v", err)
	}
	if got := config.GPULanes(provider.lanes); len(got) != 1 || got[0] != "metal" {
		t.Errorf("probed GPU lanes = %v, want [metal]", got)
	}

	samples := make([]float32, audio.SampleRate/8)
	for i := range samples {
		samples[i] = 0.2
	}

	text, err := provider.transcribe(context.Background(), samples)
	if err != nil {
		t.Fatalf("transcribe() error = %v", err)
	}
	if want := "Hello obey voice"; text != want {
		t.Fatalf("transcribe() = %q, want %q", text, want)
	}

	argsLog, err := os.ReadFile(filepath.Join(dir, "args.log"))
	if err != nil {
		t.Fatalf("ReadFile(args.log) error = %v", err)
	}
	logText := string(argsLog)
	asset, err := config.TranscribeCPPModel("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"model=" + asset.ModelPath(dir), "backend=auto", "quiet", "wav="} {
		if !strings.Contains(logText, want) {
			t.Errorf("args log %q missing %q", logText, want)
		}
	}

	// The wav and the -o transcript are per-utterance temp files; a session that
	// runs for an hour must not leave a directory behind for each turn.
	wav := strings.TrimPrefix(logText[strings.Index(logText, "wav="):], "wav=")
	if _, err := os.Stat(filepath.Dir(wav)); !os.IsNotExist(err) {
		t.Errorf("temp dir %s still exists after transcribe(); want it removed", filepath.Dir(wav))
	}
}

func TestTranscribeCPPSpecIsOfflineOnly(t *testing.T) {
	spec, ok := SpecForNormalized(config.NormalizedSTT{
		Provider: config.STTProviderTranscribeCPP,
		Mode:     config.STTModeOffline,
	})
	if !ok {
		t.Fatal("SpecForNormalized(transcribecpp/offline) not found; meeting runtime would treat it as unknown")
	}
	if spec.EmitsPartial || spec.UsesEndpoint {
		t.Errorf("spec = %+v, want utterance-final (no partials, no provider endpoint)", spec)
	}
	if !spec.RequiresVAD {
		t.Error("transcribecpp requires VAD")
	}
	if _, ok := SpecForNormalized(config.NormalizedSTT{
		Provider: config.STTProviderTranscribeCPP,
		Mode:     config.STTModeStreaming,
	}); ok {
		t.Error("transcribecpp/streaming should not be in the spec table in v1")
	}
}

// stubCapture satisfies audioSource without producing audio; NewProvider only
// needs it to be non-nil.
type stubCapture struct{}

func (stubCapture) Read() []float32 { return nil }

func TestNewProviderBuildsTranscribeCPP(t *testing.T) {
	installed, _ := transcribeCPPInstall(t, "ignored")
	installed.STTProvider = "transcribecpp"

	t.Run("VAD is required", func(t *testing.T) {
		_, _, err := NewProvider(installed, stubCapture{}, nil)
		if err == nil || !strings.Contains(err.Error(), "transcribecpp STT requires VAD") {
			t.Fatalf("error = %v, want the transcribecpp VAD requirement", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		provider, _, err := NewProvider(installed, stubCapture{}, &audio.VAD{})
		if err != nil {
			t.Fatalf("NewProvider() error = %v", err)
		}
		if _, ok := provider.(*TranscribeCPPSTT); !ok {
			t.Fatalf("provider = %T, want *TranscribeCPPSTT", provider)
		}
	})
}
