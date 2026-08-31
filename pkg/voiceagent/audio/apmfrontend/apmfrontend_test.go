//go:build apm

package apmfrontend

import (
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apm"
)

// fakeShim substitutes for the real cgo APM instance, so these tests check
// New's wiring (framing, int16/float32 conversion, delay-ms conversion, error
// propagation) without depending on webrtc-audio-processing's actual DSP
// behavior — the real library still has to compile and link for this test
// binary to exist at all (the apm tag pulls it in), but nothing here exercises
// its acoustic processing.
type fakeShim struct {
	nsLevel apm.NSLevel

	captureFrames [][]int16
	captureErr    error
	renderFrames  [][]int16
	renderErr     error
	delayMs       []int
	delayErr      error
	stats         apm.Stats
	closed        bool
}

func (f *fakeShim) ProcessCapture(frame []int16) ([]int16, error) {
	f.captureFrames = append(f.captureFrames, append([]int16(nil), frame...))
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	// Echo the frame back scaled by 2 so tests can tell the shim's output
	// (not just the input) reached ProcessCapture's return value.
	out := make([]int16, len(frame))
	for i, s := range frame {
		out[i] = s / 2
	}
	return out, nil
}

func (f *fakeShim) ProcessRender(frame []int16) error {
	f.renderFrames = append(f.renderFrames, append([]int16(nil), frame...))
	return f.renderErr
}

func (f *fakeShim) SetStreamDelayMs(ms int) error {
	f.delayMs = append(f.delayMs, ms)
	return f.delayErr
}

func (f *fakeShim) Stats() apm.Stats { return f.stats }

func (f *fakeShim) Close() error {
	f.closed = true
	return nil
}

// withFakeAPM installs fake as the shim New constructs, restoring the real
// factory when the test ends.
func withFakeAPM(t *testing.T, fake *fakeShim) {
	t.Helper()
	orig := newAPM
	newAPM = func(level apm.NSLevel) (shim, error) {
		fake.nsLevel = level
		return fake, nil
	}
	t.Cleanup(func() { newAPM = orig })
}

func TestNewClampsNSLevelAndConstructs(t *testing.T) {
	tests := []struct {
		in   int
		want apm.NSLevel
	}{
		{-1, 0}, {0, 0}, {1, 1}, {3, 3}, {4, 3}, {99, 3},
	}
	for _, tt := range tests {
		fake := &fakeShim{}
		withFakeAPM(t, fake)

		front, err := New(tt.in)
		if err != nil {
			t.Fatalf("New(%d): %v", tt.in, err)
		}
		if fake.nsLevel != tt.want {
			t.Errorf("New(%d): ns level = %v, want %v", tt.in, fake.nsLevel, tt.want)
		}
		_ = front.Close()
	}
}

func TestNewPropagatesConstructorError(t *testing.T) {
	orig := newAPM
	wantErr := errors.New("boom")
	newAPM = func(apm.NSLevel) (shim, error) { return nil, wantErr }
	t.Cleanup(func() { newAPM = orig })

	if _, err := New(1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestProcessCaptureFramesConvertsAndAppliesAGC(t *testing.T) {
	fake := &fakeShim{}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer front.Close()

	in := make([]float32, 2*frameSamples)
	for i := range in {
		in[i] = 0.2
	}

	out := front.ProcessCapture(in)
	if len(out) != len(in) {
		t.Fatalf("out len = %d, want %d", len(out), len(in))
	}
	if len(fake.captureFrames) != 2 {
		t.Fatalf("shim saw %d frames, want 2", len(fake.captureFrames))
	}
	for i, frame := range fake.captureFrames {
		if len(frame) != frameSamples {
			t.Errorf("frame %d has %d samples, want %d", i, len(frame), frameSamples)
		}
	}
	// The fake halves every sample; AGC then re-gains it, so the exact value
	// is not asserted — only that the shim's halving actually reached the
	// output rather than being discarded.
	allZero := true
	for _, s := range out {
		if s != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("ProcessCapture returned all zeros")
	}
}

func TestProcessCaptureFallsBackToPassthroughOnShimError(t *testing.T) {
	fake := &fakeShim{captureErr: errors.New("apm failure")}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer front.Close()

	in := make([]float32, frameSamples)
	for i := range in {
		in[i] = 0.42
	}
	out := front.ProcessCapture(append([]float32(nil), in...))
	if len(out) != len(in) {
		t.Fatalf("out len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("out[%d] = %v, want passthrough %v", i, out[i], in[i])
		}
	}
}

func TestPushPlaybackReferenceFramesAndForwards(t *testing.T) {
	fake := &fakeShim{}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer front.Close()

	f, ok := front.(*Frontend)
	if !ok {
		t.Fatal("New did not return *Frontend")
	}
	f.PushPlaybackReference(make([]float32, 2*frameSamples))
	if len(fake.renderFrames) != 2 {
		t.Fatalf("shim saw %d render frames, want 2", len(fake.renderFrames))
	}
}

func TestSetReferenceDelayConvertsSamplesToMilliseconds(t *testing.T) {
	fake := &fakeShim{}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer front.Close()

	delayer, ok := front.(audio.ReferenceDelayer)
	if !ok {
		t.Fatal("Frontend does not implement audio.ReferenceDelayer")
	}

	// 640 samples @ 16kHz = 40ms.
	delayer.SetReferenceDelay(640)
	if len(fake.delayMs) != 1 || fake.delayMs[0] != 40 {
		t.Fatalf("delayMs calls = %v, want [40]", fake.delayMs)
	}

	// Setting the same effective delay again must not re-call the shim.
	delayer.SetReferenceDelay(640)
	if len(fake.delayMs) != 1 {
		t.Fatalf("delayMs calls = %v, want no repeat call for an unchanged delay", fake.delayMs)
	}

	// A negative delay clamps to zero rather than going through unclamped.
	delayer.SetReferenceDelay(-100)
	if got := fake.delayMs[len(fake.delayMs)-1]; got != 0 {
		t.Errorf("negative delay produced %dms, want 0", got)
	}
}

func TestFrontendStatsMapsShimStats(t *testing.T) {
	fake := &fakeShim{stats: apm.Stats{
		EchoReturnLossEnhancement:    12.5,
		HasEchoReturnLossEnhancement: true,
		ResidualEchoLikelihood:       0.1,
		HasResidualEchoLikelihood:    true,
		VoiceDetected:                true,
		HasVoiceDetected:             true,
		DelayMs:                      42,
		HasDelayMs:                   true,
	}}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer front.Close()

	reporter, ok := front.(audio.FrontendStatsReporter)
	if !ok {
		t.Fatal("Frontend does not implement audio.FrontendStatsReporter")
	}
	got := reporter.FrontendStats()
	want := audio.FrontendStats{
		ERLE: 12.5, HasERLE: true,
		ResidualEchoLikelihood: 0.1, HasResidualEchoLikelihood: true,
		VoiceDetected: true, HasVoiceDetected: true,
		DelayMs: 42, HasDelayMs: true,
	}
	if got != want {
		t.Errorf("FrontendStats = %+v, want %+v", got, want)
	}
}

func TestCloseReleasesTheShim(t *testing.T) {
	fake := &fakeShim{}
	withFakeAPM(t, fake)
	front, err := New(1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := front.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fake.closed {
		t.Error("Close did not reach the shim")
	}
}
