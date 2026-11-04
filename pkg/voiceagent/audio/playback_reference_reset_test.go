package audio

import "testing"

func TestPlaybackReferenceResetDropsOldTimelinePreservesCaptureState(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(0) // Same delay is deliberately not a queue reset.
	f.PushPlaybackReference([]float32{.9, .8})
	f.ProcessCapture([]float32{.1})
	f.refTail = 100
	f.aec.coeffs[0] = .2
	highPass, noise, gain := f.highPass, f.ns, f.agc
	f.ResetPlaybackReference()
	if len(f.refs.samples) != 0 || f.refTail != 0 || f.aec.pos != 0 {
		t.Fatal("old reference timeline survived")
	}
	for _, v := range f.aec.hist {
		if v != 0 {
			t.Fatal("old reference delay-line sample survived")
		}
	}
	if f.aec.coeffs[0] != .2 || f.highPass != highPass || f.ns != noise || f.agc != gain {
		t.Fatal("reset altered capture processing state")
	}
	f.PushPlaybackReference([]float32{.3})
	refs := f.refs.pop(1)
	if len(refs) != 1 || refs[0] != float64(float32(.3)) {
		t.Fatalf("new timeline=%v", refs)
	}
}

func TestStartupPreserveForwardsPlaybackReferenceReset(t *testing.T) {
	primary, preserve := NewVoiceFrontend(), NewVoiceFrontend()
	primary.PushPlaybackReference([]float32{.8})
	preserve.PushPlaybackReference([]float32{.7})
	wrapper := newStartupPreserveFrontend(primary, preserve)
	wrapper.ResetPlaybackReference()
	if len(primary.refs.samples) != 0 || len(preserve.refs.samples) != 0 {
		t.Fatal("wrapper retained reference PCM")
	}
}
