package audio

import "testing"

// TestVoiceFrontendPreservesNormalSpeechImmediately verifies that startup
// noise estimation does not require several seconds of warmup before speech
// can reach VAD/STT.
func TestVoiceFrontendPreservesNormalSpeechImmediately(t *testing.T) {
	const frame = ChunkSize
	speech := make([]float32, frame)
	for i := range speech {
		speech[i] = voicedSample(i, 0.12) // normal speaking level
	}
	inRMS := frameRMS(speech)

	out := NewVoiceFrontend().ProcessCapture(append([]float32(nil), speech...))
	survived := frameRMS(out) / inRMS

	if survived <= 0.2 {
		t.Fatalf("normal speech survived at %.2fx on first frame, want > 0.2x", survived)
	}
}
