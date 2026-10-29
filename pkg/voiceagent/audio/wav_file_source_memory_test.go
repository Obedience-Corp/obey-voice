package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeSparseWAV writes a canonical 44-byte header that declares sampleCount
// samples and then extends the file to match without writing any samples. On
// APFS and ext4 the extension is sparse, so a two-hour file costs no disk and
// reads back as silence.
func writeSparseWAV(tb testing.TB, path string, sampleCount int64) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	dataBytes := uint32(sampleCount * 2)
	header := make([]byte, 0, 44)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, 36+dataBytes)
	header = append(header, "WAVE"...)
	header = append(header, "fmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16)
	header = binary.LittleEndian.AppendUint16(header, 1) // PCM
	header = binary.LittleEndian.AppendUint16(header, 1) // mono
	header = binary.LittleEndian.AppendUint32(header, SampleRate)
	header = binary.LittleEndian.AppendUint32(header, SampleRate*2)
	header = binary.LittleEndian.AppendUint16(header, 2)
	header = binary.LittleEndian.AppendUint16(header, 16)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, dataBytes)
	if _, err := f.Write(header); err != nil {
		tb.Fatalf("write header: %v", err)
	}
	if err := f.Truncate(int64(len(header)) + int64(dataBytes)); err != nil {
		tb.Fatalf("extend %s: %v", path, err)
	}
}

// TestWAVFileSourceHoldsBoundedMemoryOverTwoHours streams a two-hour file and
// watches the heap while it does. The point is the shape of the memory curve,
// not the absolute number: a source that buffered the file would grow past the
// margin within the first few minutes.
func TestWAVFileSourceHoldsBoundedMemoryOverTwoHours(t *testing.T) {
	if testing.Short() {
		t.Skip("streams a two-hour file; skipped under -short")
	}
	const (
		sampleCount  = int64(2 * 60 * 60 * SampleRate)
		sampleEvery  = 1000 // frames between heap samples
		marginBytes  = 8 << 20
		wantMinutes  = 120
		bytesPerMiB  = 1 << 20
		framesPerMin = 60 * SampleRate / ChunkSize
	)
	path := filepath.Join(t.TempDir(), "two-hours.wav")
	writeSparseWAV(t, path, sampleCount)

	src, err := NewWAVFileSource(path, ChunkSize)
	if err != nil {
		t.Fatalf("NewWAVFileSource: %v", err)
	}
	defer src.Close()
	if got := src.TotalSamples(); got != sampleCount {
		t.Fatalf("TotalSamples = %d, want %d", got, sampleCount)
	}

	heapInUse := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapInuse
	}
	baseline := heapInUse()

	ctx := t.Context()
	var frames int64
	peak := baseline
	for {
		frame, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame after %d frames: %v", frames, err)
		}
		if frame.Final {
			break
		}
		frames++
		if frames%sampleEvery != 0 {
			continue
		}
		heap := heapInUse()
		if heap > peak {
			peak = heap
		}
		if heap > baseline+marginBytes {
			t.Fatalf("heap in use is %d MiB after %d frames (%d minutes of audio), baseline %d MiB: audio is being retained",
				heap/bytesPerMiB, frames, frames/framesPerMin, baseline/bytesPerMiB)
		}
	}

	wantFrames := sampleCount / ChunkSize
	if frames != wantFrames {
		t.Fatalf("read %d frames, want %d", frames, wantFrames)
	}
	if src.Position() != sampleCount {
		t.Fatalf("Position = %d at EOF, want %d", src.Position(), sampleCount)
	}
	t.Logf("%d minutes streamed in %d frames; heap in use baseline %d KiB, peak %d KiB",
		wantMinutes, frames, baseline/1024, peak/1024)
}

// BenchmarkWAVFileSourceReadFrame reports the per-frame allocation cost. One
// frame is one fresh []float32; the read buffer is reused.
func BenchmarkWAVFileSourceReadFrame(b *testing.B) {
	path := filepath.Join(b.TempDir(), "ten-minutes.wav")
	writeSparseWAV(b, path, 10*60*SampleRate)

	open := func() *WAVFileSource {
		src, err := NewWAVFileSource(path, ChunkSize)
		if err != nil {
			b.Fatalf("NewWAVFileSource: %v", err)
		}
		return src
	}
	src := open()
	defer func() { _ = src.Close() }()

	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		frame, err := src.ReadFrame(ctx)
		if err != nil {
			b.Fatalf("ReadFrame: %v", err)
		}
		if frame.Final {
			_ = src.Close()
			src = open()
		}
	}
}
