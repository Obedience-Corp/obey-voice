package audio

import (
	"testing"
)

// A burst that starts after silence must re-prime the offset: the queue drains
// to empty between utterances, and without re-priming every sentence after the
// first would be uncompensated.
func TestReferenceDelayRePrimesAfterSilence(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(320)

	push := make([]float32, 160)
	for i := range push {
		push[i] = 0.5
	}

	// First burst: the offset is primed, so the first pops are the silent
	// device-buffer window rather than far-end
	f.PushPlaybackReference(push)
	if out := f.refsSnapshot(); len(out) != 320+160 {
		t.Fatalf("queue depth after first push = %d, want 320 delay + 160 pushed", len(out))
	}

	// Drain it the way continuous capture does during silence.
	for range 10 {
		f.ProcessCapture(make([]float32, 160))
	}
	if got := len(f.refsSnapshot()); got != 0 {
		t.Fatalf("queue depth after silence = %d, want 0 (drained)", got)
	}

	// Next burst re-primes rather than pairing immediately.
	f.PushPlaybackReference(push)
	if out := f.refsSnapshot(); len(out) != 320+160 {
		t.Fatalf("queue depth after second burst = %d, want the offset re-primed", len(out))
	}
}
