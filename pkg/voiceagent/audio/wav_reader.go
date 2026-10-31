package audio

import (
	"io"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
)

// WAVFormat describes PCM sample geometry without loading audio samples.
// The alias preserves the audio package API for existing playback callers.
type WAVFormat = wav.WAVFormat

// ReadWAVFormat delegates dependency-free WAV inspection to the shared reader.
func ReadWAVFormat(r io.ReadSeeker, size int64) (WAVFormat, error) {
	return wav.ReadWAVFormat(r, size)
}
