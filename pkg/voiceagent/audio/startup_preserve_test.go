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
