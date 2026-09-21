package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// UnsupportedWAVError reports a WAV whose format the file source will not read.
// The source neither resamples nor downmixes, so the caller decides what to do
// with a file that does not already match the capture format. It carries the
// format that was found so a command can report the mismatch.
type UnsupportedWAVError struct {
	Path   string
	Format WAVFormat
}

func (e *UnsupportedWAVError) Error() string {
	return fmt.Sprintf("audio: %s is %d Hz, %d channel(s), %d-bit PCM; want %d Hz, 1 channel, 16-bit PCM",
		e.Path, e.Format.SampleRate, e.Format.Channels, e.Format.BitsPerSample, SampleRate)
}

// WAVFileSource streams a finite 16 kHz mono 16-bit PCM WAV as Frames. It holds
// one chunk of audio at a time, so its memory cost does not depend on the
// file's length.
type WAVFileSource struct {
	path      string
	file      *os.File
	format    WAVFormat
	chunk     int    // samples per frame
	raw       []byte // reusable read buffer, reused across frames
	total     int64  // total samples in the file
	pos       int64  // samples delivered so far
	seq       int64
	finalSent bool
	closed    bool
}

// NewWAVFileSource opens path and positions it at the first sample.
// chunkSamples is the number of samples per frame; 0 selects ChunkSize. The
// file must already be 16 kHz mono 16-bit PCM: anything else returns
// *UnsupportedWAVError naming the format that was found.
func NewWAVFileSource(path string, chunkSamples int) (*WAVFileSource, error) {
	if chunkSamples <= 0 {
		chunkSamples = ChunkSize
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("audio: open wav: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("audio: stat wav %s: %w", path, err)
	}
	format, err := ReadWAVFormat(file, info.Size())
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("audio: %s: %w", path, err)
	}
	if format.SampleRate != SampleRate || format.Channels != Channels || format.BitsPerSample != 16 {
		file.Close()
		return nil, &UnsupportedWAVError{Path: path, Format: format}
	}
	if _, err := file.Seek(format.DataOffset, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("audio: seek to samples in %s: %w", path, err)
	}
	return &WAVFileSource{
		path:   path,
		file:   file,
		format: format,
		chunk:  chunkSamples,
		raw:    make([]byte, chunkSamples*2),
		total:  format.Frames(),
	}, nil
}

// ReadFrame implements FrameSource. Each call returns the next chunk as a
// SourceFile frame; end of input is one explicit Final frame, and every read
// after that returns ErrSourceClosed.
func (s *WAVFileSource) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if s.closed {
		return Frame{}, ErrSourceClosed
	}

	remaining := s.total - s.pos
	if remaining <= 0 {
		if s.finalSent {
			return Frame{}, ErrSourceClosed
		}
		s.finalSent = true
		return Frame{SourceKind: SourceFile, Final: true}, nil
	}

	n := int(min(int64(s.chunk), remaining))
	raw := s.raw[:n*2]
	if _, err := io.ReadFull(s.file, raw); err != nil {
		return Frame{}, fmt.Errorf("audio: read %s at sample %d: %w", s.path, s.pos, err)
	}

	// A fresh slice per frame: the buffer above is reused, but a consumer that
	// retains samples (the VAD pre-roll ring does) must not see them rewritten.
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(raw[i*2:]))) / float32(math.MaxInt16)
	}
	s.pos += int64(n)
	s.seq++

	return Frame{
		Samples:    samples,
		SampleRate: s.format.SampleRate,
		Channels:   s.format.Channels,
		Duration:   SamplesDuration(n),
		Sequence:   s.seq,
		SourceKind: SourceFile,
	}, nil
}

// Close releases the file. It is safe to call more than once.
func (s *WAVFileSource) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("audio: close %s: %w", s.path, err)
	}
	return nil
}

// Position reports how many samples have been delivered.
func (s *WAVFileSource) Position() int64 { return s.pos }

// TotalSamples reports how many samples the file holds.
func (s *WAVFileSource) TotalSamples() int64 { return s.total }

// Format reports the file's header, for a caller that has to check it against a
// request.
func (s *WAVFileSource) Format() WAVFormat { return s.format }
