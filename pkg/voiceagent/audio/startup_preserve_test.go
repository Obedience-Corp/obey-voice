package audio

import "testing"

type rmsFrontend struct{ gain float64 }

func (f rmsFrontend) ProcessCapture(samples []float32) []float32 {
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = float32(float64(s) * f.gain)
	}
	return out
}

func (rmsFrontend) PushPlaybackReference([]float32) {}

func (rmsFrontend) Close() error { return nil }

func TestStartupPreserveUsesNLMSWhenPrimaryOverSuppresses(t *testing.T) {
	f := newStartupPreserveFrontend(rmsFrontend{gain: 0.05}, rmsFrontend{gain: 1})
	speech := make([]float32, 960)
	for i := range speech {
		speech[i] = 0.04
	}
	out := f.ProcessCapture(speech)
	if frameRMS(out) > 0.01 {
		t.Fatalf("primary output changed: RMS %.4f", frameRMS(out))
	}
	if !f.NearEnd() {
		t.Fatal("expected NearEnd when NLMS residual dominates APM")
	}
}

func TestStartupPreserveKeepsPrimaryWhenNLMSIsQuiet(t *testing.T) {
	f := newStartupPreserveFrontend(rmsFrontend{gain: 0.05}, rmsFrontend{gain: 0.1})
	echo := make([]float32, 960)
	for i := range echo {
		echo[i] = 0.04
	}
	out := f.ProcessCapture(echo)
	if frameRMS(out) > StartupPreserveMinRMS {
		t.Fatalf("echo output RMS %.4f, want primary residual", frameRMS(out))
	}
}

func TestStartupPreserveLastResidualIsIndependentCopy(t *testing.T) {
	f := newStartupPreserveFrontend(rmsFrontend{gain: 0.05}, rmsFrontend{gain: 1})
	speech := make([]float32, 960)
	for i := range speech {
		speech[i] = 0.04
	}
	_ = f.ProcessCapture(speech)
	got := f.LastResidual()
	if len(got) != 960 || got[0] != 0.04 {
		t.Fatalf("LastResidual = %v", got[:1])
	}
	got[0] = 1
	again := f.LastResidual()
	if again[0] != 0.04 {
		t.Fatal("LastResidual shared backing storage")
	}
}

func TestCropNearEndResidualKeepsFirstHopPad(t *testing.T) {
	n := 12 * 960
	residual := make([]float32, n)
	primary := make([]float32, n)
	for i := range primary {
		primary[i] = 0.002
	}
	// First NearEnd hop at 180ms (3*960), matching live T172324Z.
	for i := 3 * 960; i < 4*960; i++ {
		residual[i] = 0.04
	}
	got := CropNearEndResidual(residual, primary)
	start := 3*960 - nearEndPreRoll
	end := 3*960 + nearEndHold
	if len(got) != end-start {
		t.Fatalf("crop len=%d, want %d", len(got), end-start)
	}
	if got[nearEndPreRoll] != 0.04 || got[0] != 0 {
		t.Fatal("crop is not first NearEnd hop plus pre-roll")
	}
}

func TestNormalizeSpeechRMSRaisesQuietCrop(t *testing.T) {
	in := make([]float32, 4800)
	for i := range in {
		in[i] = 0.01
	}
	got := NormalizeSpeechRMS(in)
	if frameRMS(got) < 0.07 || frameRMS(got) > 0.09 {
		t.Fatalf("normalized RMS %.4f, want ~0.08", frameRMS(got))
	}
}

func TestCropNearEndResidualRejectsEchoOnly(t *testing.T) {
	n := 12 * 960
	residual := make([]float32, n)
	primary := make([]float32, n)
	for i := range n {
		residual[i] = 0.004
		primary[i] = 0.005
	}
	if CropNearEndResidual(residual, primary) != nil {
		t.Fatal("echo-only residual must not crop a NearEnd window")
	}
}
