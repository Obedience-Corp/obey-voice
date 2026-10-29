package audio

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSourceWAV writes samples as a canonical mono 16 kHz WAV and returns the path.
func writeSourceWAV(t *testing.T, samples []float32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.wav")
	if err := WriteWAVFloat32(path, SampleRate, samples); err != nil {
		t.Fatalf("WriteWAVFloat32: %v", err)
	}
	return path
}

func ramp(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.Sin(float64(i) * 0.05))
	}
	return out
}

func TestWAVFileSourceStreamsEverySample(t *testing.T) {
	want := ramp(2500)
	src, err := NewWAVFileSource(writeSourceWAV(t, want), 1000)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	defer src.Close()

	if src.TotalSamples() != int64(len(want)) {
		t.Fatalf("TotalSamples = %d, want %d", src.TotalSamples(), len(want))
	}
	gotFmt := src.Format()
	if gotFmt.SampleRate != SampleRate || gotFmt.Channels != Channels || gotFmt.BitsPerSample != 16 {
		t.Fatalf("Format() = %+v, want %d Hz, %d channel, 16-bit", gotFmt, SampleRate, Channels)
	}
	if gotFmt.DataBytes != int64(len(want)*2) {
		t.Fatalf("Format().DataBytes = %d, want %d", gotFmt.DataBytes, len(want)*2)
	}

	var got []float32
	var sizes []int
	ctx := t.Context()
	for {
		frame, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if frame.Final {
			break
		}
		if frame.SourceKind != SourceFile {
			t.Fatalf("SourceKind = %q, want %q", frame.SourceKind, SourceFile)
		}
		if frame.SampleRate != SampleRate || frame.Channels != Channels {
			t.Fatalf("frame is %d Hz %d channels", frame.SampleRate, frame.Channels)
		}
		if frame.Sequence != int64(len(sizes)+1) {
			t.Fatalf("Sequence = %d, want %d", frame.Sequence, len(sizes)+1)
		}
		sizes = append(sizes, len(frame.Samples))
		got = append(got, frame.Samples...)
	}

	if wantSizes := []int{1000, 1000, 500}; !slices.Equal(sizes, wantSizes) {
		t.Fatalf("frame sizes = %v, want %v", sizes, wantSizes)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if diff := math.Abs(float64(got[i] - want[i])); diff > 1e-4 {
			t.Fatalf("sample %d = %v, want %v (16-bit round trip)", i, got[i], want[i])
		}
	}
	if src.Position() != src.TotalSamples() {
		t.Errorf("Position = %d at EOF, want TotalSamples %d", src.Position(), src.TotalSamples())
	}

	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("read after Final = %v, want ErrSourceClosed", err)
	}
}

func TestWAVFileSourceEmptyFileIsOneFinalFrame(t *testing.T) {
	src, err := NewWAVFileSource(writeSourceWAV(t, nil), 1000)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	defer src.Close()

	ctx := t.Context()
	frame, err := src.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !frame.Final || len(frame.Samples) != 0 {
		t.Fatalf("first frame = %+v, want an empty Final frame", frame)
	}
	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("read after empty Final = %v, want ErrSourceClosed", err)
	}
}

func TestWAVFileSourceRejectsUnsupportedFormats(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, spec wavSpec) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, buildWAV(spec), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "48 kHz",
			path: write("48k.wav", wavSpec{tag: 1, channels: 1, bits: 16, rate: 48000, dataBytes: 3200, dataPresent: 3200}),
			want: "48000 Hz",
		},
		{
			name: "stereo",
			path: write("stereo.wav", wavSpec{tag: 1, channels: 2, bits: 16, rate: 16000, dataBytes: 3200, dataPresent: 3200}),
			want: "2 channel(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewWAVFileSource(tc.path, 1000)
			if _, ok := errors.AsType[*UnsupportedWAVError](err); !ok {
				t.Fatalf("error = %v, want *UnsupportedWAVError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestWAVFileSourceRejectsTruncatedHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.wav")
	if err := os.WriteFile(path, buildWAV(wavSpec{tag: 1, channels: 1, bits: 16, rate: 16000, dataBytes: 3200, dataPresent: 100}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWAVFileSource(path, 1000)
	if err == nil || !strings.Contains(err.Error(), "only 100 remain") {
		t.Fatalf("error = %v, want a truncated-data rejection", err)
	}
}

func TestWAVFileSourceReportsAShrinkingFile(t *testing.T) {
	path := writeSourceWAV(t, ramp(4000))
	src, err := NewWAVFileSource(path, 1000)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	defer src.Close()

	if _, err := src.ReadFrame(t.Context()); err != nil {
		t.Fatalf("first ReadFrame: %v", err)
	}
	if err := os.Truncate(path, 44+2000); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = src.ReadFrame(t.Context())
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want a read failure naming %s", err, path)
	}
}

func TestWAVFileSourceHonorsCancellation(t *testing.T) {
	src, err := NewWAVFileSource(writeSourceWAV(t, ramp(4000)), 1000)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	defer src.Close()

	ctx, cancel := context.WithCancel(t.Context())
	if _, err := src.ReadFrame(ctx); err != nil {
		t.Fatalf("first ReadFrame: %v", err)
	}
	cancel()
	if _, err := src.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadFrame after cancel = %v, want context.Canceled", err)
	}
	if src.Position() != 1000 {
		t.Errorf("Position = %d after one frame and a cancel, want 1000", src.Position())
	}
}

func TestWAVFileSourceReadAfterClose(t *testing.T) {
	src, err := NewWAVFileSource(writeSourceWAV(t, ramp(4000)), 1000)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := src.ReadFrame(t.Context()); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("ReadFrame after Close = %v, want ErrSourceClosed", err)
	}
}
