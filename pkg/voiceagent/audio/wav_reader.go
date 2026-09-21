package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// WAVFormat describes a PCM WAV file's sample layout and where its sample
// data lives, without holding any samples.
type WAVFormat struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	// DataOffset is the byte offset of the first sample in the file.
	DataOffset int64
	// DataBytes is the length of the sample data in bytes.
	DataBytes int64
}

// BlockAlign is the size of one frame (one sample per channel) in bytes.
func (f WAVFormat) BlockAlign() int { return f.Channels * f.BitsPerSample / 8 }

// Frames is the number of frames in the data chunk.
func (f WAVFormat) Frames() int64 {
	if f.BlockAlign() == 0 {
		return 0
	}
	return f.DataBytes / int64(f.BlockAlign())
}

// ReadWAVFormat walks the RIFF chunks of a 16-bit PCM WAV and returns its
// format and data location. It never reads the data payload, so its cost does
// not depend on file length. size is the total length of r in bytes.
func ReadWAVFormat(r io.ReadSeeker, size int64) (WAVFormat, error) {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return WAVFormat{}, fmt.Errorf("audio: read wav header: %w", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return WAVFormat{}, fmt.Errorf("audio: not a RIFF/WAVE file")
	}

	var format WAVFormat
	var haveFmt, haveData bool
	pos := int64(len(header))

	for !haveData {
		var chunk [8]byte
		if _, err := io.ReadFull(r, chunk[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return WAVFormat{}, fmt.Errorf("audio: read wav chunk header: %w", err)
		}
		pos += int64(len(chunk))
		id := string(chunk[0:4])
		length := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		if remaining := size - pos; length > remaining {
			return WAVFormat{}, fmt.Errorf("audio: wav chunk %q declares %d bytes but only %d remain", id, length, remaining)
		}

		switch id {
		case "fmt ":
			if length < 16 {
				return WAVFormat{}, fmt.Errorf("audio: wav fmt chunk is %d bytes, want at least 16", length)
			}
			var body [16]byte
			if _, err := io.ReadFull(r, body[:]); err != nil {
				return WAVFormat{}, fmt.Errorf("audio: read wav fmt chunk: %w", err)
			}
			if tag := binary.LittleEndian.Uint16(body[0:2]); tag != 1 {
				return WAVFormat{}, fmt.Errorf("audio: unsupported wav format tag %d, want 1 (PCM)", tag)
			}
			format.Channels = int(binary.LittleEndian.Uint16(body[2:4]))
			format.SampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			format.BitsPerSample = int(binary.LittleEndian.Uint16(body[14:16]))
			haveFmt = true
			if _, err := r.Seek(length-int64(len(body)), io.SeekCurrent); err != nil {
				return WAVFormat{}, fmt.Errorf("audio: seek past wav fmt chunk: %w", err)
			}
		case "data":
			if !haveFmt {
				return WAVFormat{}, fmt.Errorf("audio: wav data chunk appears before fmt chunk")
			}
			format.DataOffset = pos
			format.DataBytes = length
			haveData = true
			continue // do not read or skip the payload
		default:
			if _, err := r.Seek(length, io.SeekCurrent); err != nil {
				return WAVFormat{}, fmt.Errorf("audio: seek past wav chunk %q: %w", id, err)
			}
		}
		pos += length
		if length%2 == 1 {
			if _, err := r.Seek(1, io.SeekCurrent); err != nil {
				return WAVFormat{}, fmt.Errorf("audio: seek wav chunk padding: %w", err)
			}
			pos++
		}
	}

	switch {
	case !haveFmt || !haveData:
		return WAVFormat{}, fmt.Errorf("audio: wav is missing its fmt or data chunk")
	case format.Channels == 0:
		return WAVFormat{}, fmt.Errorf("audio: wav declares 0 channels")
	case format.SampleRate == 0:
		return WAVFormat{}, fmt.Errorf("audio: wav declares a sample rate of 0")
	case format.BitsPerSample != 16:
		return WAVFormat{}, fmt.Errorf("audio: unsupported wav bit depth %d, want 16", format.BitsPerSample)
	case format.DataBytes%int64(format.BlockAlign()) != 0:
		return WAVFormat{}, fmt.Errorf("audio: wav data length %d is not a whole number of %d-byte frames", format.DataBytes, format.BlockAlign())
	}
	return format, nil
}
