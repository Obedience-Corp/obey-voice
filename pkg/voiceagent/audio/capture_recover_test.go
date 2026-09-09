package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDevice replaces the malgo open/close seam so reopen behavior is
// exercised without hardware.
type fakeDevice struct {
	mu     sync.Mutex
	opens  int
	closes int
	err    error
	logs   []string
}

func (f *fakeDevice) install(c *Capture) {
	c.open = func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.opens++
		return f.err
	}
	c.closeDevice = func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closes++
	}
	c.Logf = func(format string, args ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
	}
}

func (f *fakeDevice) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeDevice) counts() (opens, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.closes
}

func (f *fakeDevice) logged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

// starve makes the next ReadFrame see a stalled stream.
func starve(c *Capture) {
	c.lastData.Store(time.Now().Add(-2 * captureStallTimeout).UnixNano())
}

// A Capture nobody configured must not write anywhere: recovery runs while a
// Bubble Tea program may own the terminal, and the standalone meeting recorder
// leaves file descriptor 2 pointed at it.
func TestCaptureDefaultLoggerIsSilent(t *testing.T) {
	c := NewCapture()
	if c.Logf != nil {
		t.Fatal("a new Capture must have no logger until a caller picks one")
	}
	fail := false
	c.open = func() error {
		if fail {
			return errors.New(`audio device "gone" is not available`)
		}
		return nil
	}
	c.closeDevice = func() {}

	var stderr bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&stderr)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	fail = true
	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame during a failed reopen = %v, want ErrNoFrameReady", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("an unconfigured Capture wrote %q", stderr.String())
	}
}

func TestCaptureReadFrameReopensAfterStall(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	var dev fakeDevice
	dev.install(c)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame during recovery = %v, want ErrNoFrameReady", err)
	}
	opens, closes := dev.counts()
	if opens != 2 || closes != 1 {
		t.Fatalf("opens=%d closes=%d, want the stalled device closed and reopened once", opens, closes)
	}
	if !c.IsRunning() {
		t.Fatal("capture must be running again after a successful reopen")
	}
	if logs := dev.logged(); len(logs) != 1 {
		t.Fatalf("reopen must be logged, got %q", logs)
	}
}

func TestCaptureReadFrameRetriesAfterAFailedReopen(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	var dev fakeDevice
	dev.install(c)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	// A PipeWire graph restart or an unplugged USB mic: the reopen fails.
	dev.failWith(errors.New(`audio device "obey-review-no-such-device" is not available`))
	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("a failed reopen must not be fatal to the STT loop, got %v", err)
	}
	if c.IsRunning() {
		t.Fatal("no device is open after a failed reopen")
	}

	// The recorder used to show "listening" against a dead microphone forever:
	// nothing retried. A later stall window must.
	dev.failWith(nil)
	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame during recovery = %v, want ErrNoFrameReady", err)
	}
	if !c.IsRunning() {
		t.Fatal("a later stall window must reopen the device once it is back")
	}
	opens, _ := dev.counts()
	if opens != 3 {
		t.Fatalf("opens = %d, want start plus a failed and a successful reopen", opens)
	}
	logs := dev.logged()
	if len(logs) != 2 {
		t.Fatalf("both reopen attempts must be logged, got %q", logs)
	}
	if !strings.Contains(logs[0], "reopen failed") {
		t.Fatalf("the failed attempt must say so, got %q", logs[0])
	}
}

func TestCaptureStoppedIsNeverReopened(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	var dev fakeDevice
	dev.install(c)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	c.Stop()

	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame on a stopped capture = %v, want ErrNoFrameReady", err)
	}
	opens, _ := dev.counts()
	if opens != 1 {
		t.Fatalf("opens = %d, want no reopen after the caller stopped capture", opens)
	}
	if c.IsRunning() {
		t.Fatal("a stopped capture must stay stopped")
	}
}

func TestCaptureRestartAfterStopStillStopsOnCancel(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	var dev fakeDevice
	dev.install(c)

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("first start: %v", err)
	}
	c.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for c.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.IsRunning() {
		t.Fatal("a Start after a full Stop must get its own cancellation watcher")
	}
}

func TestCaptureReopenStopsTheNewDeviceWhenContextIsCancelled(t *testing.T) {
	t.Parallel()
	c := NewCapture()
	var dev fakeDevice
	dev.install(c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel in the window between closing the stalled device and opening the
	// new one: the cancellation watcher fires while nothing is open, so only
	// the reopen itself can stop what it just started.
	var recovering atomic.Bool
	open := c.open
	c.open = func() error {
		if recovering.Load() {
			cancel()
		}
		return open()
	}

	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	recovering.Store(true)
	starve(c)
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame during recovery = %v, want ErrNoFrameReady", err)
	}
	if c.IsRunning() {
		t.Fatal("a context cancelled during the reopen must leave no device running")
	}
}
