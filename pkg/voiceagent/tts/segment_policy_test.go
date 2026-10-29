package tts

import "testing"

// The grouping policy is two numbers per engine, and both are measurements
// with a date on them (kokoro.go, qwen/policy.go). A change here changes how
// much text every reply is cut into, so it has to be deliberate.
func TestSegmentPolicyConstantsArePinned(t *testing.T) {
	kokoro := &KokoroTTS{}
	if got := SegmentRampOf(kokoro); got != 1.5 {
		t.Errorf("Kokoro SegmentRamp = %v, want 1.5 (1/0.64 measured RTF)", got)
	}
	if JoinsSentencesOf(kokoro) {
		t.Error("Kokoro JoinsSentences = true, want false: sherpa re-splits sentence punctuation inside one call")
	}
	// Audio8 declares nothing, so it takes the default: an unmeasured engine
	// speaks its text as written rather than having its punctuation rewritten.
	if !JoinsSentencesOf(&Audio8TTS{}) {
		t.Error("Audio8 JoinsSentencesOf = false, want the request-join default")
	}
}
