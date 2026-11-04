package playback

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"sync"
	"testing"
	"time"
)

type fakeOutput struct {
	mu            sync.Mutex
	stops, closes int
}

func (f *fakeOutput) Stop() error { f.mu.Lock(); defer f.mu.Unlock(); f.stops++; return nil }
func (f *fakeOutput) Uninit()     { f.mu.Lock(); defer f.mu.Unlock(); f.closes++ }

func TestResetOutputPreservesPlayerAndFencesOldPumps(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	p.SetVolume(.37)
	device := &fakeOutput{}
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { return device, rate, 2, nil }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	oldEpoch := p.epoch
	segment := newPlaybackSegment()
	segment.append([]int16{1200, 1200})
	segment.finishInput(nil)
	p.mu.Lock()
	p.queue = append(p.queue, segment)
	p.mu.Unlock()
	if err := p.ResetOutput(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.closed || p.device != nil || p.sampleRate != 0 || p.observedPeriodFrames.Load() != 0 {
		t.Fatal("reset retained native state or closed player")
	}
	if p.volumeHundredths.Load() != 37 {
		t.Fatal("reset lost volume")
	}
	if _, err := p.ensureDeviceGeneration(24000, &oldEpoch); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale pump reopened: %v", err)
	}
	out := []byte{1, 1, 1, 1}
	p.onData(out, nil, 2)
	for _, v := range out {
		if v != 0 {
			t.Fatal("stale PCM rendered")
		}
	}
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatalf("lazy reopen: %v", err)
	}
	if result := <-segment.doneCh; !result.Interrupted {
		t.Fatal("queued segment not interrupted")
	}
}

func TestRecoveryRetriesWithoutReplayingAndStopsAtBudget(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	calls := 0
	p.testOpen = func(rate int) (nativeOutput, int, int, error) {
		calls++
		if calls > 1 {
			return nil, 0, 0, errors.New("missing output")
		}
		return &fakeOutput{}, rate, 2, nil
	}
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	generation := p.generation
	p.recoverOutput(recoverySignal{generation, "unexpected_stop"}, make(chan struct{}))
	if calls != 4 {
		t.Fatalf("got %d opens, want initial + 3", calls)
	}
	status := p.OutputStatus()
	if status.State != "failed" || status.RecoveryAttempts != 3 || status.LastError == "" {
		t.Fatalf("missing persistent failure: %+v", status)
	}
	p.recoverOutput(recoverySignal{generation, "unexpected_stop"}, make(chan struct{}))
	if calls != 4 {
		t.Fatal("stale recovery retried")
	}
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal("failed reopen not retryable", err)
	}
}

func TestResetCancelsRegisteredInFlightWorker(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := newPlaybackSegment()
	p.mu.Lock()
	p.workers = map[*playbackSegment]context.CancelFunc{s: cancel}
	p.mu.Unlock()
	if err := p.ResetOutput(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("pump not canceled")
	}
}

func TestConcurrentResetAndClose(t *testing.T) {
	p := NewPlayer()
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.ResetOutput(context.Background()) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Close() }()
	wg.Wait()
	if p.OutputStatus().State != "closed" {
		t.Fatal("close lost to reset")
	}
	if _, err := p.ensureDevice(24000); err == nil {
		t.Fatal("closed player reopened")
	}
}

func TestHeartbeatDetectsMissingCallbacks(t *testing.T) {
	p := NewPlayer()
	now := time.Unix(100, 0)
	if p.callbackStalled(now) {
		t.Fatal("unopened device has no heartbeat")
	}
	p.lastCallback.Store(now.Add(-2 * time.Second).UnixNano())
	if p.callbackStalled(now) {
		t.Fatal("premature recovery")
	}
	p.lastCallback.Store(now.Add(-4 * time.Second).UnixNano())
	if !p.callbackStalled(now) {
		t.Fatal("missed callback stall")
	}
}

func TestCloseDuringRecoveryBackoff(t *testing.T) {
	p := NewPlayer()
	failed := make(chan struct{})
	calls := 0
	p.testOpen = func(rate int) (nativeOutput, int, int, error) {
		calls++
		if calls > 1 {
			if calls == 2 {
				close(failed)
			}
			return nil, 0, 0, errors.New("unavailable")
		}
		return &fakeOutput{}, rate, 2, nil
	}
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	p.signalRecovery(p.generation, "unexpected_stop")
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	// Close stops the worker's backoff before another native open can occur.
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	if calls != 2 {
		t.Fatalf("unexpected opens after close: %d", calls)
	}
}

func TestPlayStreamSuccessfulBufferedAndIncremental(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "incremental"}[incremental], func(t *testing.T) {
			p := NewPlayer()
			defer p.Close()
			p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stream := audio.NewPCMStream(ctx)
			if err := stream.SetSampleRate(24000); err != nil {
				t.Fatal(err)
			}
			stream.SetIncremental(incremental)
			go func() { stream.Write([]float32{.2, .3, .4, .5}); stream.Close() }()
			playback, err := p.PlayStream(ctx, stream)
			if err != nil {
				t.Fatal("successful stream rejected", err)
			}
			if playback == nil {
				t.Fatal("missing playback")
			}
			out := make([]byte, 16)
			p.onData(out, nil, 4)
			nonzero := false
			for _, b := range out {
				nonzero = nonzero || b != 0
			}
			if !nonzero {
				t.Fatal("admitted stream rendered silence")
			}
		})
	}
}
func TestRepeatedSuccessfulReopensHaveFiniteBudget(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	calls := 0
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { calls++; return &fakeOutput{}, rate, 2, nil }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		p.mu.Lock()
		generation := p.generation
		p.mu.Unlock()
		p.recoverOutput(recoverySignal{generation, "unexpected_stop"}, make(chan struct{}))
	}
	if calls != 4 {
		t.Fatalf("unstable route spun beyond budget: %d opens", calls)
	}
	if p.OutputStatus().State != "failed" {
		t.Fatal("unstable route not surfaced")
	}
}

type resetAwareFrontend struct{ resets int }

func (f *resetAwareFrontend) ProcessCapture(samples []float32) []float32 { return samples }
func (f *resetAwareFrontend) PushPlaybackReference([]float32)            {}
func (f *resetAwareFrontend) Close() error                               { return nil }
func (f *resetAwareFrontend) ResetPlaybackReference()                    { f.resets++ }
func TestResetRefreshesReferenceWithoutClosingFrontend(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	f := &resetAwareFrontend{}
	p.SetFrontend(f)
	old := p.aecIngress
	if err := p.ResetOutput(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.resets != 1 || p.frontend != f || p.aecIngress == old || p.aecIngress == nil {
		t.Fatal("frontend reference generation not refreshed")
	}
}

func TestResetDrainsLateProducerWithoutReopening(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)
	result := make(chan error, 1)
	go func() { _, err := p.PlayStream(ctx, stream); result <- err }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		registered := len(p.workers) > 0
		p.mu.Unlock()
		if registered {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("worker not registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := p.ResetOutput(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late worker result %v", err)
	}
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if err := stream.Write([]float32{.1}); err != nil {
			t.Fatal("late producer blocked", err)
		}
	}
	stream.Close()
	if p.device != nil {
		t.Fatal("late producer reopened output")
	}
}

func TestResetWinsBlockedFailedOpenStatus(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	p.testOpen = func(int) (nativeOutput, int, int, error) {
		close(entered)
		<-release
		return nil, 0, 0, errors.New("open failed")
	}
	opened := make(chan error, 1)
	go func() { _, err := p.ensureDevice(24000); opened <- err }()
	<-entered
	reset := make(chan error, 1)
	go func() { reset <- p.ResetOutput(context.Background()) }()
	close(release)
	if err := <-opened; err == nil {
		t.Fatal("fake open unexpectedly succeeded")
	}
	if err := <-reset; err != nil {
		t.Fatal(err)
	}
	if status := p.OutputStatus(); status.State != "idle" || status.LastError != "" {
		t.Fatalf("stale failure overwrote reset: %+v", status)
	}
}

func TestRouteChangeRecoversOnceForSameNameDifferentIdentity(t *testing.T) {
	p := NewPlayer()
	defer p.Close()
	reopened := make(chan struct{}, 1)
	calls := 0
	p.testOpen = func(rate int) (nativeOutput, int, int, error) {
		calls++
		if calls > 1 {
			reopened <- struct{}{}
		}
		return &fakeOutput{}, rate, 2, nil
	}
	p.testRoute = func() (string, error) { return "second-backend-identity", nil }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.routeID = "first-backend-identity"
	generation := p.generation
	p.mu.Unlock()
	p.checkOutputRoute(generation)
	p.checkOutputRoute(generation)
	select {
	case <-reopened:
	case <-time.After(time.Second):
		t.Fatal("route change did not recover")
	}
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	if calls != 2 {
		t.Fatalf("duplicate recovery: %d opens", calls)
	}
}

func TestMissingNamedRouteFailsClosedAndRemainsRetryable(t *testing.T) {
	p := NewPlayerWithDevice("explicit speaker")
	defer p.Close()
	exhausted := make(chan struct{})
	calls := 0
	p.testOpen = func(rate int) (nativeOutput, int, int, error) {
		calls++
		if calls > 1 {
			if calls == 4 {
				close(exhausted)
			}
			return nil, 0, 0, errors.New("explicit speaker is not available")
		}
		return &fakeOutput{}, rate, 2, nil
	}
	p.testRoute = func() (string, error) { return "", errors.New("explicit speaker is not available") }
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal(err)
	}
	p.checkOutputRoute(p.generation)
	select {
	case <-exhausted:
	case <-time.After(2 * time.Second):
		t.Fatal("named disappearance not surfaced")
	}
	p.deviceMu.Lock()
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
	p.deviceMu.Unlock()
	status := p.OutputStatus()
	if status.State != "failed" || status.Device.RequestedName != "explicit speaker" {
		t.Fatalf("missing named route was substituted: %+v", status)
	}
	if _, err := p.ensureDevice(24000); err != nil {
		t.Fatal("named reconnection not retryable", err)
	}
}
