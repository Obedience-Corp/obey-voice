package qwen

import "testing"

// Qwen's ramp is 1/1.83, its measured in-pipeline RTF at 0.6b, and its join is
// a real request join. Both are read by the pipeline's grouper every turn.
func TestSegmentPolicyConstantsArePinned(t *testing.T) {
	q := &Qwen3TTS{}
	if got := q.SegmentRamp(); got != 0.55 {
		t.Errorf("SegmentRamp = %v, want 0.55 (1/1.83 in-pipeline RTF)", got)
	}
	if !q.JoinsSentences() {
		t.Error("JoinsSentences = false, want true: decoding is autoregressive over the whole request")
	}
}
