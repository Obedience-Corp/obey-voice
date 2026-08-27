package speaker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// fakeSortformerCLI writes a transcribe-cli stand-in that answers
// --list-devices with devices, records its argv and the preset env var, and
// prints body on stdout the way --batch-jsonl does.
func fakeSortformerCLI(t *testing.T, dir, body, devices string) string {
	t.Helper()
	path := filepath.Join(dir, "transcribe-cli")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--list-devices" ]; then
  cat <<'DEVICES'
` + devices + `DEVICES
  exit 0
fi
printf '%s preset=%s\n' "$*" "${TRANSCRIBE_SORTFORMER_STREAM_PRESET:-unset}" > "` + dir + `/args.log"
cat <<'JSONL'
` + body + `JSONL
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// sortformerInstall lays out a models dir holding the managed diarizer GGUF and
// returns options pointing at it plus the fake CLI's temp dir.
func sortformerInstall(t *testing.T, body, devices string) (SortformerOptions, string) {
	t.Helper()
	dir := t.TempDir()
	modelPath := config.SortformerDiarizerAsset().ModelPath(dir)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return SortformerOptions{
		Binary:    fakeSortformerCLI(t, dir, body, devices),
		ModelPath: modelPath,
		ModelRev:  config.SortformerModelRevision,
	}, dir
}

func fixtureJSONL(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sortformer-batch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNewSortformerDiarizerFailsBeforeTheMeetingStarts(t *testing.T) {
	installed, dir := sortformerInstall(t, "", gpuDeviceList)

	broken := filepath.Join(t.TempDir(), "transcribe-cli")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		mutate  func(*SortformerOptions)
		wantMsg string
	}{
		{
			name:    "binary not configured",
			mutate:  func(o *SortformerOptions) { o.Binary = "" },
			wantMsg: "transcribecpp_binary is not set",
		},
		{
			name:    "missing binary",
			mutate:  func(o *SortformerOptions) { o.Binary = filepath.Join(dir, "absent-cli") },
			wantMsg: "not found in PATH",
		},
		{
			name:    "missing gguf",
			mutate:  func(o *SortformerOptions) { o.ModelPath = filepath.Join(t.TempDir(), "absent.gguf") },
			wantMsg: "obey-voice models ensure",
		},
		{
			name:    "binary cannot list devices",
			mutate:  func(o *SortformerOptions) { o.Binary = broken },
			wantMsg: "--list-devices",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := installed
			tt.mutate(&opts)
			if _, err := NewSortformerDiarizer(context.Background(), opts); err == nil {
				t.Fatal("NewSortformerDiarizer() error = nil, want construction failure")
			} else if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.wantMsg)
			}
		})
	}
}

// A CPU-only machine is refused for transcribe.cpp STT but not for diarization:
// Sortformer on CPU is still 2.6x the sherpa review path it replaces (T3).
func TestNewSortformerDiarizerAcceptsACPUOnlyMachine(t *testing.T) {
	opts, _ := sortformerInstall(t, "", cpuOnlyDeviceList)
	if _, err := NewSortformerDiarizer(context.Background(), opts); err != nil {
		t.Fatalf("NewSortformerDiarizer() error = %v, want a CPU-only machine to be usable", err)
	}
}

func TestSortformerDiarizeMapsBatchJSONLOntoObservations(t *testing.T) {
	opts, dir := sortformerInstall(t, fixtureJSONL(t), gpuDeviceList)
	diarizer, err := NewSortformerDiarizer(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewSortformerDiarizer() error = %v", err)
	}

	// numSpeakers is deliberately non-zero: Sortformer has no count input and
	// must neither pass it on nor pretend to honor it.
	timeline, err := diarizer.Diarize(context.Background(), make([]float32, 16000), 3)
	if err != nil {
		t.Fatalf("Diarize() error = %v", err)
	}

	want := []Observation{
		{SegmentID: "sortformer-1", StartMS: 0, EndMS: 560, Label: "speaker-1"},
		{SegmentID: "sortformer-2", StartMS: 880, EndMS: 1520, Label: "speaker-1"},
		{SegmentID: "sortformer-3", StartMS: 2800, EndMS: 5120, Label: "speaker-1"},
		{SegmentID: "sortformer-4", StartMS: 66880, EndMS: 71520, Label: "speaker-1"},
		// The CLI emits this span last (it groups by speaker); review reads
		// observations in order, so it belongs here.
		{SegmentID: "sortformer-5", StartMS: 72320, EndMS: 75920, Label: "speaker-2"},
		{SegmentID: "sortformer-6", StartMS: 76800, EndMS: 77360, Label: "speaker-1"},
		{SegmentID: "sortformer-7", StartMS: 85360, EndMS: 87600, Label: "speaker-1"},
		{SegmentID: "sortformer-8", StartMS: 88240, EndMS: 89360, Label: "speaker-1"},
	}
	if len(timeline.Observations) != len(want) {
		t.Fatalf("got %d observations, want %d", len(timeline.Observations), len(want))
	}
	for i, obs := range timeline.Observations {
		if obs.SegmentID != want[i].SegmentID || obs.StartMS != want[i].StartMS ||
			obs.EndMS != want[i].EndMS || obs.Label != want[i].Label {
			t.Errorf("observation %d = %+v, want %+v", i, obs, want[i])
		}
		if obs.State != StateStable || obs.Source != SourceRecording {
			t.Errorf("observation %d state/source = %s/%s, want stable/recording", i, obs.State, obs.Source)
		}
		if obs.ModelRev != config.SortformerModelRevision {
			t.Errorf("observation %d model revision = %q, want %q", i, obs.ModelRev, config.SortformerModelRevision)
		}
	}

	args, err := os.ReadFile(filepath.Join(dir, "args.log"))
	if err != nil {
		t.Fatalf("ReadFile(args.log) error = %v", err)
	}
	argv := string(args)
	for _, want := range []string{
		"-m " + opts.ModelPath, "--backend auto", "-q", "--batch ", "--batch-jsonl",
		"preset=" + sortformerPreset,
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q missing %q", argv, want)
		}
	}
	// numSpeakers reached no flag: the model has no speaker-count input.
	if strings.Contains(argv, "speakers") || strings.Contains(argv, " 3") {
		t.Errorf("argv %q passed a speaker count Sortformer does not accept", argv)
	}
	// The wav and its batch list are per-run temp files; a long-lived agent
	// must not leave a directory behind for every meeting it reviews.
	batchList := ""
	fields := strings.Fields(argv)
	for i, field := range fields {
		if field == "--batch" && i+1 < len(fields) {
			batchList = fields[i+1]
		}
	}
	if batchList == "" {
		t.Fatalf("argv %q carries no --batch list", argv)
	}
	if _, err := os.Stat(filepath.Dir(batchList)); !os.IsNotExist(err) {
		t.Errorf("temp dir %s still exists after Diarize(); want it removed", filepath.Dir(batchList))
	}
}

func TestSortformerDiarizeCancels(t *testing.T) {
	dir := t.TempDir()
	modelPath := config.SortformerDiarizerAsset().ModelPath(dir)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A CLI that answers --list-devices but then hangs, standing in for a long
	// review the user gives up on with Ctrl+C.
	binary := filepath.Join(dir, "transcribe-cli")
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--list-devices\" ]; then\n  cat <<'DEVICES'\n" +
		gpuDeviceList + "DEVICES\n  exit 0\nfi\nsleep 120\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	diarizer, err := NewSortformerDiarizer(context.Background(), SortformerOptions{Binary: binary, ModelPath: modelPath})
	if err != nil {
		t.Fatalf("NewSortformerDiarizer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := diarizer.Diarize(ctx, make([]float32, 16000), 0)
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Diarize() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Diarize() did not return after the context was cancelled")
	}
}

func TestSortformerDiarizeShortCircuits(t *testing.T) {
	opts, dir := sortformerInstall(t, fixtureJSONL(t), gpuDeviceList)
	diarizer, err := NewSortformerDiarizer(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewSortformerDiarizer() error = %v", err)
	}

	t.Run("cancelled context never runs the CLI", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := diarizer.Diarize(ctx, make([]float32, 16000), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("Diarize() error = %v, want context.Canceled", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "args.log")); !os.IsNotExist(err) {
			t.Error("Diarize() ran the CLI for a cancelled context")
		}
	})

	t.Run("no audio is an empty timeline", func(t *testing.T) {
		timeline, err := diarizer.Diarize(context.Background(), nil, 0)
		if err != nil {
			t.Fatalf("Diarize() error = %v", err)
		}
		if len(timeline.Observations) != 0 {
			t.Fatalf("observations = %+v, want none", timeline.Observations)
		}
	})
}

func TestParseSortformerJSONLRejectsWhatTheContractCannotExpress(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantErr bool
		wantIDs []string
	}{
		{
			name:    "malformed line",
			out:     "{\"speakers\":[}\n",
			wantErr: true,
		},
		{
			name: "empty and reversed spans are dropped",
			out: `{"speakers":[{"t0_ms":100,"t1_ms":100,"speaker_id":1},` +
				`{"t0_ms":900,"t1_ms":400,"speaker_id":1},` +
				`{"t0_ms":1000,"t1_ms":2000,"speaker_id":2}]}` + "\n",
			wantIDs: []string{"sortformer-1"},
		},
		{
			name:    "speaker ids below one are dropped rather than relabelled",
			out:     `{"speakers":[{"t0_ms":0,"t1_ms":500,"speaker_id":0}]}` + "\n",
			wantIDs: nil,
		},
		{
			name:    "a header-only run is an empty timeline",
			out:     `{"type":"batch_header","load_ms":88.9}` + "\n",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeline, err := parseSortformerJSONL([]byte(tt.out), "rev")
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseSortformerJSONL() error = nil, want a parse failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSortformerJSONL() error = %v", err)
			}
			if len(timeline.Observations) != len(tt.wantIDs) {
				t.Fatalf("observations = %+v, want %d", timeline.Observations, len(tt.wantIDs))
			}
			for i, id := range tt.wantIDs {
				if timeline.Observations[i].SegmentID != id {
					t.Errorf("observation %d id = %q, want %q", i, timeline.Observations[i].SegmentID, id)
				}
			}
		})
	}
}

func TestSortformerOptionsFromConfig(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/models", TranscribeCPPBinary: "transcribe-cli"}
	opts := SortformerOptionsFromConfig(cfg, "/models")
	if opts.Binary != "transcribe-cli" {
		t.Errorf("Binary = %q, want the configured transcribe.cpp CLI", opts.Binary)
	}
	if want := config.SortformerDiarizerAsset().ModelPath("/models"); opts.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", opts.ModelPath, want)
	}
	if opts.ModelRev != config.SortformerModelRevision {
		t.Errorf("ModelRev = %q, want %q", opts.ModelRev, config.SortformerModelRevision)
	}
}
