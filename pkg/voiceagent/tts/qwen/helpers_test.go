package qwen

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func drainStream(t *testing.T, s *audio.PCMStream) []float32 {
	t.Helper()
	var samples []float32
	for frame := range s.Frames() {
		samples = append(samples, frame...)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
	return samples
}
