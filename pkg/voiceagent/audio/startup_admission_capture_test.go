package audio

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestCaptureQuietTailOverwriteFeedsNormalizedCropToReadFrame(t *testing.T) {
	c := NewCapture()
	primary := constPCM(12*960, 0.002)
	residual := constPCM(12*960, 0)
	for i := 3 * 960; i < 4*960; i++ {
		residual[i] = 0.04
	}
	ingestResidualFrames(t, c, primary, residual, constPCM(960, 0.03))
	keep := 750 * time.Millisecond
	_, crop := c.SnapshotStartupWindows(keep)
	if len(crop) == 0 {
		t.Fatal("expected NearEnd crop")
	}
	want := PadStartupListen(NormalizeSpeechRMS(crop))
	c.ReplaceUnreadWithRaw(keep)
	quietP := constPCM(960, 0.002)
	quietR := constPCM(960, 0)
	at := time.Now()
	for i := range 8 {
		c.SetFrontend(residualFrontend{primary: quietP, residual: quietR})
		c.ingestCapture(constPCM(960, 0.002), at.Add(time.Duration(i)*60*time.Millisecond), 1)
	}
	c.ReplaceUnread(want)
	got := readUnreadFrames(t, c)
	if !slices.Equal(got, want) {
		t.Fatalf("STT PCM len=%d want %d (normalized NearEnd crop)", len(got), len(want))
	}
}

func TestCaptureEchoOnlyQuietTailDoesNotOverwriteWithWaitCrop(t *testing.T) {
	c := NewCapture()
	primary := constPCM(12*960, 0.005)
	residual := constPCM(12*960, 0.004)
	ingestResidualFrames(t, c, primary, residual, constPCM(960, 0.03))
	keep := 750 * time.Millisecond
	_, crop := c.SnapshotStartupWindows(keep)
	if crop != nil {
		t.Fatal("echo-only residual must not crop a NearEnd window")
	}
	c.ReplaceUnreadWithRaw(keep)
	quietP := constPCM(960, 0.002)
	quietR := constPCM(960, 0)
	at := time.Now()
	for i := range 8 {
		c.SetFrontend(residualFrontend{primary: quietP, residual: quietR})
		c.ingestCapture(constPCM(960, 0.002), at.Add(time.Duration(i)*60*time.Millisecond), 1)
	}
	got := readUnreadFrames(t, c)
	if len(got) == 0 {
		t.Fatal("echo-only keep window was empty")
	}
	if frameRMS(got) >= frontendTargetRMS-0.01 {
		t.Fatal("echo-only overwrite installed a normalized Wait crop")
	}
}

func TestCaptureSnapshotStartupWindowsCropsAfterUnreadDrain(t *testing.T) {
	c := NewCapture()
	primary := constPCM(12*960, 0.002)
	residual := constPCM(12*960, 0)
	for i := 3 * 960; i < 4*960; i++ {
		residual[i] = 0.04
	}
	ingestResidualFrames(t, c, primary, residual, constPCM(960, 0.03))
	if _, err := c.ReadFrame(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, crop := c.SnapshotStartupWindows(0)
	unreadP := primary[ChunkSize:]
	unreadR := residual[ChunkSize:]
	n := alignedKeepSamples(len(unreadP))
	want := CropNearEndResidual(unreadR[len(unreadR)-n:], unreadP[len(unreadP)-n:])
	if !slices.Equal(crop, want) {
		t.Fatalf("crop after ReadFrame len=%d want %d", len(crop), len(want))
	}
}

func TestCaptureSnapshotStartupWindowsHopAlignsUnalignedKeep(t *testing.T) {
	c := NewCapture()
	n := 13 * 960
	primary := constPCM(n, 0.002)
	residual := constPCM(n, 0)
	for i := 3 * 960; i < 4*960; i++ {
		residual[i] = 0.04
	}
	ingestResidualFrames(t, c, primary, residual, constPCM(960, 0.03))
	_, crop := c.SnapshotStartupWindows(750 * time.Millisecond)
	keep := alignedKeepSamples(min(n, int(750*time.Millisecond*SampleRate/time.Second)))
	want := CropNearEndResidual(residual[n-keep:], primary[n-keep:])
	if !slices.Equal(crop, want) {
		t.Fatalf("aligned crop len=%d want %d", len(crop), len(want))
	}
}

func ingestResidualFrames(t *testing.T, c *Capture, primary, residual, raw []float32) {
	t.Helper()
	if len(primary) != len(residual) || len(primary)%960 != 0 {
		t.Fatalf("primary/residual len=%d/%d", len(primary), len(residual))
	}
	at := time.Now()
	for i := 0; i < len(primary); i += 960 {
		c.SetFrontend(residualFrontend{primary: primary[i : i+960], residual: residual[i : i+960]})
		c.ingestCapture(slices.Clone(raw), at.Add(time.Duration(i/960)*60*time.Millisecond), 1)
	}
}

func readUnreadFrames(t *testing.T, c *Capture) []float32 {
	t.Helper()
	var got []float32
	for {
		f, err := c.ReadFrame(t.Context())
		if errors.Is(err, ErrNoFrameReady) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f.Samples...)
	}
}

func constPCM(n int, v float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = v
	}
	return out
}
