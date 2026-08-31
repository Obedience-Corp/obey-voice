package apmfrontend

import "testing"

// passthroughProcess is a fake "shim" that returns the frame unchanged, so
// framing correctness can be checked independently of what APM itself does
// to the samples.
func passthroughProcess(frame []int16) ([]int16, error) {
	out := append([]int16(nil), frame...)
	return out, nil
}

func TestCaptureFramerExactMultiple(t *testing.T) {
	f := &captureFramer{}
	// Production shape: 1600 samples (100ms @ 16kHz), an exact multiple of
	// the 160-sample frame — the case that must never see a shortfall.
	in := make([]float32, 10*frameSamples)
	for i := range in {
		in[i] = 0.5
	}

	var calls int
	out, err := f.Process(in, func(frame []int16) ([]int16, error) {
		calls++
		if len(frame) != frameSamples {
			t.Fatalf("frame %d has %d samples, want %d", calls, len(frame), frameSamples)
		}
		return passthroughProcess(frame)
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if calls != 10 {
		t.Errorf("calls = %d, want 10", calls)
	}
	if len(out) != len(in) {
		t.Fatalf("out len = %d, want %d", len(out), len(in))
	}
	for i, s := range out {
		if s == 0 {
			t.Fatalf("out[%d] = 0, want the passthrough input to survive", i)
		}
	}
	if len(f.pending) != 0 {
		t.Errorf("pending = %d samples, want 0 after an exact multiple", len(f.pending))
	}
}

func TestCaptureFramerNonMultipleCarriesRemainder(t *testing.T) {
	f := &captureFramer{}
	// Not a multiple of 160: exercises the partial-frame path production
	// traffic never hits.
	first := make([]float32, 250) // one full frame + 90 pending
	for i := range first {
		first[i] = 0.25
	}

	out1, err := f.Process(first, passthroughProcess)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out1) != len(first) {
		t.Fatalf("out1 len = %d, want %d", len(out1), len(first))
	}
	// Only one frame (160 samples) could have finished; the rest of this
	// call's output is the documented silence shortfall.
	nonZero := 0
	for _, s := range out1 {
		if s != 0 {
			nonZero++
		}
	}
	if nonZero != frameSamples {
		t.Errorf("non-zero samples in out1 = %d, want %d (one processed frame)", nonZero, frameSamples)
	}
	if len(f.pending) != 90 {
		t.Fatalf("pending = %d, want 90", len(f.pending))
	}

	// A second call that completes the pending frame must flush the
	// carried-over backlog rather than losing it.
	second := make([]float32, 70) // 90+70 = 160, exactly one more frame
	for i := range second {
		second[i] = 0.25
	}
	out2, err := f.Process(second, passthroughProcess)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out2) != len(second) {
		t.Fatalf("out2 len = %d, want %d", len(out2), len(second))
	}
	for i, s := range out2 {
		if s == 0 {
			t.Fatalf("out2[%d] = 0, want the completed frame's output to have started draining", i)
		}
	}
	if len(f.pending) != 0 {
		t.Errorf("pending = %d, want 0 once the frame completed", len(f.pending))
	}
}

func TestCaptureFramerPropagatesProcessError(t *testing.T) {
	f := &captureFramer{}
	in := make([]float32, frameSamples)

	wantErr := errFake
	_, err := f.Process(in, func([]int16) ([]int16, error) { return nil, wantErr })
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestRenderFramerBuffersAndFlushes(t *testing.T) {
	f := &renderFramer{}
	var got [][]int16

	// Fewer than one frame: nothing should fire yet.
	if err := f.Push(make([]float32, 50), func(frame []int16) error {
		got = append(got, frame)
		return nil
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("fired %d frames before a full one accumulated", len(got))
	}

	// Completing the frame (50 + 110 = 160) must fire exactly once.
	if err := f.Push(make([]float32, 110), func(frame []int16) error {
		got = append(got, frame)
		if len(frame) != frameSamples {
			t.Fatalf("frame has %d samples, want %d", len(frame), frameSamples)
		}
		return nil
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("fired %d frames, want 1", len(got))
	}
	if len(f.pending) != 0 {
		t.Errorf("pending = %d, want 0", len(f.pending))
	}
}

func TestRenderFramerPropagatesProcessError(t *testing.T) {
	f := &renderFramer{}
	err := f.Push(make([]float32, frameSamples), func([]int16) error { return errFake })
	if err != errFake {
		t.Fatalf("err = %v, want %v", err, errFake)
	}
}

func TestFloat32Int16RoundTrip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1, -1, 1.5, -1.5}
	out := int16ToFloat32(float32ToInt16(in))
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	// Out-of-range inputs must clamp rather than wrap.
	if out[5] > 1.0001 {
		t.Errorf("1.5 did not clamp: got %v", out[5])
	}
	if out[6] < -1.0001 {
		t.Errorf("-1.5 did not clamp: got %v", out[6])
	}
	// In-range values survive the round trip within int16 quantization.
	for i := 0; i < 5; i++ {
		diff := float64(out[i]) - float64(in[i])
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.001 {
			t.Errorf("in[%d] = %v round-tripped to %v", i, in[i], out[i])
		}
	}
}

// errFake is a sentinel used by tests to check error propagation without
// depending on any particular error type.
type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

var errFake = &fakeError{"fake process error"}
