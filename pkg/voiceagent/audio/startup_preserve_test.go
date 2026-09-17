package audio

import (
	"slices"
	"testing"
)

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

type gatedPreserveFrontend struct {
	aec, out []float32
}

func (f gatedPreserveFrontend) ProcessCapture([]float32) []float32 { return slices.Clone(f.out) }

func (gatedPreserveFrontend) PushPlaybackReference([]float32) {}

func (gatedPreserveFrontend) Close() error { return nil }

func (f gatedPreserveFrontend) LastAECResidual() []float32 { return slices.Clone(f.aec) }

func TestStartupPreserveUsesAECResidualWhenNSGates(t *testing.T) {
	aec := make([]float32, 960)
	gated := make([]float32, 960)
	for i := range aec {
		aec[i] = 0.04
		gated[i] = 0.002
	}
	f := newStartupPreserveFrontend(rmsFrontend{gain: 0.05}, gatedPreserveFrontend{aec: aec, out: gated})
	speech := make([]float32, 960)
	for i := range speech {
		speech[i] = 0.04
	}
	_ = f.ProcessCapture(speech)
	if !f.NearEnd() {
		t.Fatal("duplex NS on the preserve path hid the NearEnd hop")
	}
	got := f.LastResidual()
	if frameRMS(got) < StartupPreserveMinRMS {
		t.Fatalf("LastResidual RMS %.4f is the gated NS output", frameRMS(got))
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

func TestAlignedKeepSamplesTrimsToHopFromNewest(t *testing.T) {
	if got := alignedKeepSamples(12000); got != 11520 {
		t.Fatalf("aligned keep=%d, want 11520", got)
	}
	if got := alignedKeepSamples(nearEndMinKeep - 1); got != nearEndMinKeep-1 {
		t.Fatalf("short keep=%d", got)
	}
}

func TestPadToCaptureFramesDoesNotMixPartialLastChunk(t *testing.T) {
	in := make([]float32, 4000)
	for i := range in {
		in[i] = 0.04
	}
	got := PadToCaptureFrames(in)
	if len(got) != 4800 {
		t.Fatalf("padded len=%d, want 4800", len(got))
	}
	if got[3999] != 0.04 || got[4000] != 0 {
		t.Fatal("pad overwrote the crop or left the tail non-silent")
	}
}

func TestPadStartupListenAppendsTrailingSilence(t *testing.T) {
	in := make([]float32, 4800)
	for i := range in {
		in[i] = 0.04
	}
	got := PadStartupListen(in)
	if len(got) != 4800+startupDecoderSilence {
		t.Fatalf("listen pad len=%d", len(got))
	}
	if got[4799] != 0.04 || got[4800] != 0 || got[len(got)-1] != 0 {
		t.Fatal("startup listen pad did not keep the crop followed by silence")
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
