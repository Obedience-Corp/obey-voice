package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// wavHeaderSize is the fixed byte length of the canonical 44-byte mono PCM16
// WAV header written by WAVWriter (matching WriteWAVFloat32's layout).
const wavHeaderSize = 44

// WAVWriter streams mono 16-bit PCM to a WAV file incrementally. Each Write
// appends samples and patches the RIFF/data size fields in place, so a file
// left behind by a crash mid-recording is still a valid, readable WAV up to the
// last flushed chunk. This lets long recordings avoid buffering the whole
// capture in memory: callers stream chunks straight to disk and re-read the
// file when the complete PCM is finally needed (e.g. offline diarization).
//
// A WAVWriter is not safe for concurrent Writes; callers that fan in from
// multiple goroutines must serialize, exactly as the meeting capture loop does.
type WAVWriter struct {
	f       *os.File
	sampleN uint32
	closed  bool
}

// NewWAVWriter creates path (private, 0600) and writes a 44-byte header with
// zero-length data. Samples are appended by Write and the sizes are finalized
// on Close (and after every Write for crash resilience).
func NewWAVWriter(path string, sampleRate int) (*WAVWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("wav: invalid sample rate %d", sampleRate)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	w := &WAVWriter{f: f}
	if err := w.writeHeader(sampleRate); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return w, nil
}

// OpenWAVWriter appends to an existing mono 16-bit WAV written by WAVWriter.
// Used to resume meeting audio into the same file instead of truncating it.
func OpenWAVWriter(path string, sampleRate int) (*WAVWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("wav: invalid sample rate %d", sampleRate)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var hdr [wavHeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("wav: read header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		_ = f.Close()
		return nil, fmt.Errorf("wav: %s is not a PCM WAV", path)
	}
	if binary.LittleEndian.Uint16(hdr[22:24]) != 1 {
		_ = f.Close()
		return nil, fmt.Errorf("wav: %s is not mono", path)
	}
	gotRate := int(binary.LittleEndian.Uint32(hdr[24:28]))
	if gotRate != sampleRate {
		_ = f.Close()
		return nil, fmt.Errorf("wav: sample rate %d, want %d", gotRate, sampleRate)
	}
	dataSize := binary.LittleEndian.Uint32(hdr[40:44])
	if dataSize%2 != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("wav: odd data size %d", dataSize)
	}
	end := int64(wavHeaderSize) + int64(dataSize)
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("wav: seek data end: %w", err)
	}
	return &WAVWriter{f: f, sampleN: dataSize / 2}, nil
}

func (w *WAVWriter) writeHeader(sampleRate int) error {
	var h [wavHeaderSize]byte
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36) // riff size for zero data
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(h[22:24], 1)  // mono
	binary.LittleEndian.PutUint32(h[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(h[32:34], 2)                    // block align
	binary.LittleEndian.PutUint16(h[34:36], 16)                   // bits/sample
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], 0) // data size for zero data
	_, err := w.f.Write(h[:])
	return err
}

// Write appends samples as little-endian int16 and patches the size fields so
// the file stays valid at every chunk boundary. Values are clamped to [-1, 1].
func (w *WAVWriter) Write(samples []float32) error {
	if w == nil || w.closed {
		return fmt.Errorf("wav: write to closed writer")
	}
	if len(samples) == 0 {
		return nil
	}
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := int16(clampFloat(float64(s), -1.0, 1.0) * float64(math.MaxInt16))
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	// Append at the current end-of-file offset; patchSizes uses WriteAt and
	// leaves this offset untouched so the next Write continues appending.
	if _, err := w.f.Write(buf); err != nil {
		return err
	}
	w.sampleN += uint32(len(samples))
	return w.patchSizes()
}

// patchSizes rewrites the RIFF and data length fields to match sampleN.
func (w *WAVWriter) patchSizes() error {
	dataSize := w.sampleN * 2
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], 36+dataSize)
	if _, err := w.f.WriteAt(b[:], 4); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(b[:], dataSize)
	if _, err := w.f.WriteAt(b[:], 40); err != nil {
		return err
	}
	return nil
}

// Samples reports how many samples have been written so far.
func (w *WAVWriter) Samples() int {
	if w == nil {
		return 0
	}
	return int(w.sampleN)
}

// Close finalizes the size fields and closes the file. Safe to call once; a
// second call is a no-op.
func (w *WAVWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	err := w.patchSizes()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// MixAt adds samples at a position on a mono timeline. Gaps are silent and
// overlaps are summed with PCM16 saturation. Like Write, this runs on a disk
// worker, never a device callback.
func (w *WAVWriter) MixAt(samples []float32, offset int64) error {
	if w == nil || w.closed {
		return fmt.Errorf("wav: mix into closed writer")
	}
	if offset < 0 || offset+int64(len(samples)) > math.MaxUint32/2 {
		return fmt.Errorf("wav: timeline exceeds PCM WAV limit")
	}
	if len(samples) == 0 {
		return nil
	}
	buf := make([]byte, len(samples)*2)
	if _, err := w.f.ReadAt(buf, wavHeaderSize+offset*2); err != nil && err != io.EOF {
		return err
	}
	for i, s := range samples {
		old := float64(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / 32768
		value := int16(clampFloat(old+float64(s), -1, 1) * math.MaxInt16)
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(value))
	}
	if _, err := w.f.WriteAt(buf, wavHeaderSize+offset*2); err != nil {
		return err
	}
	if end := offset + int64(len(samples)); end > int64(w.sampleN) {
		w.sampleN = uint32(end)
	}
	return w.patchSizes()
}

// SetSamples clips or pads the timeline to a recording's stop boundary.
func (w *WAVWriter) SetSamples(samples int64) error {
	if w == nil || w.closed {
		return fmt.Errorf("wav: resize closed writer")
	}
	if samples < 0 || samples > math.MaxUint32/2 {
		return fmt.Errorf("wav: timeline exceeds PCM WAV limit")
	}
	if err := w.f.Truncate(wavHeaderSize + samples*2); err != nil {
		return err
	}
	w.sampleN = uint32(samples)
	return w.patchSizes()
}

// Sync makes recording data durable before its bundle is marked sealed.
func (w *WAVWriter) Sync() error { return w.f.Sync() }
