package audio

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestCaptureSetDeviceRetainsCaptureStateAndDeliversFrames(t *testing.T) {
	c := NewCaptureWithDevice("mic one")
	var mu sync.Mutex
	var opened []string
	c.open = func() error {
		mu.Lock()
		opened = append(opened, c.deviceName)
		mu.Unlock()
		return nil
	}
	c.closeDevice = func() {}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	id, subscription := c.Subscribe(1)
	defer c.Unsubscribe(id)
	c.buf.Write(make([]float32, ChunkSize))

	if err := c.SetDevice(context.Background(), "mic two"); err != nil {
		t.Fatalf("set device: %v", err)
	}
	if got := c.buf.Available(); got != 0 {
		t.Fatalf("stale ring audio = %d samples, want empty", got)
	}
	if got := c.deviceName; got != "mic two" {
		t.Fatalf("device name = %q, want %q", got, "mic two")
	}
	if got := len(opened); got != 2 {
		t.Fatalf("opens = %d, want initial open plus rebind", got)
	}

	samples := make([]float32, ChunkSize)
	c.buf.Write(samples)
	c.publish(samples)
	if _, err := c.ReadFrame(context.Background()); err != nil {
		t.Fatalf("read rebound frame: %v", err)
	}
	select {
	case got := <-subscription:
		if len(got) != ChunkSize {
			t.Fatalf("subscription frame length = %d, want %d", len(got), ChunkSize)
		}
	default:
		t.Fatal("existing subscription did not receive a rebound frame")
	}
}

func TestCaptureSetDeviceNamedToDefault(t *testing.T) {
	c := NewCaptureWithDevice("named")
	var opened []string
	c.open = func() error {
		opened = append(opened, c.deviceName)
		return nil
	}
	c.closeDevice = func() {}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	if err := c.SetDevice(context.Background(), ""); err != nil {
		t.Fatalf("set default device: %v", err)
	}
	if got := c.deviceName; got != "" {
		t.Fatalf("device name = %q, want system default", got)
	}
	if len(opened) != 2 || opened[1] != "" {
		t.Fatalf("opened devices = %#v, want named then system default", opened)
	}
}

func TestCaptureSetDeviceSameNameIsNoOp(t *testing.T) {
	c := NewCaptureWithDevice("same")
	opens, closes := 0, 0
	c.open = func() error {
		opens++
		return nil
	}
	c.closeDevice = func() { closes++ }

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	if err := c.SetDevice(context.Background(), "same"); err != nil {
		t.Fatalf("set same device: %v", err)
	}
	if opens != 1 || closes != 0 {
		t.Fatalf("opens=%d closes=%d, want 1 and 0", opens, closes)
	}
}

func TestCaptureSetDeviceFailureRestoresPreviousDevice(t *testing.T) {
	c := NewCaptureWithDevice("working")
	selectionErr := errors.New("selected mic unavailable")
	var opened []string
	c.open = func() error {
		opened = append(opened, c.deviceName)
		if c.deviceName == "missing" {
			return selectionErr
		}
		return nil
	}
	c.closeDevice = func() {}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	defer c.Stop()
	err := c.SetDevice(context.Background(), "missing")
	if !errors.Is(err, selectionErr) {
		t.Fatalf("set missing device error = %v, want %v", err, selectionErr)
	}
	if c.deviceName != "working" || !c.IsRunning() {
		t.Fatalf("rollback left name=%q running=%v, want working and running", c.deviceName, c.IsRunning())
	}
	if len(opened) != 3 {
		t.Fatalf("opened devices = %#v, want initial, failed selection, rollback", opened)
	}
}

func TestCaptureSetDeviceRollbackFailureKeepsCaptureRecoverable(t *testing.T) {
	c := NewCaptureWithDevice("working")
	selectionErr := errors.New("selected mic unavailable")
	rollbackErr := errors.New("old mic still unavailable")
	var opens []string
	c.open = func() error {
		opens = append(opens, c.deviceName)
		switch c.deviceName {
		case "missing":
			return selectionErr
		case "working":
			if len(opens) > 1 {
				return rollbackErr
			}
		}
		return nil
	}
	c.closeDevice = func() {}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()
	err := c.SetDevice(context.Background(), "missing")
	if !errors.Is(err, selectionErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v, want selection and rollback errors", err)
	}
	if c.deviceName != "working" || c.IsRunning() {
		t.Fatalf("failed rollback left name=%q running=%v, want working and stopped", c.deviceName, c.IsRunning())
	}
}

func TestCaptureSetDeviceCancellationDuringOpenRestoresPrevious(t *testing.T) {
	c := NewCaptureWithDevice("working")
	openNew := make(chan struct{})
	releaseNew := make(chan struct{})
	var opens []string
	c.open = func() error {
		opens = append(opens, c.deviceName)
		if c.deviceName == "new" {
			close(openNew)
			<-releaseNew
		}
		return nil
	}
	c.closeDevice = func() {}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.SetDevice(ctx, "new") }()

	<-openNew
	cancel()
	close(releaseNew)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled set error = %v, want context.Canceled", err)
	}
	if c.deviceName != "working" || !c.IsRunning() {
		t.Fatalf("cancelled open left name=%q running=%v, want working and running", c.deviceName, c.IsRunning())
	}
	c.Stop()
}

func TestCaptureSetDeviceStoppedAndCancelled(t *testing.T) {
	c := NewCaptureWithDevice("initial")
	opens := 0
	c.open = func() error {
		opens++
		return nil
	}
	c.closeDevice = func() {}

	if err := c.SetDevice(context.Background(), "next"); err == nil {
		t.Fatal("stopped capture reported a successful switch")
	}
	if c.deviceName != "initial" || c.IsRunning() || opens != 0 {
		t.Fatalf("stopped set left name=%q running=%v opens=%d", c.deviceName, c.IsRunning(), opens)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SetDevice(ctx, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled set error = %v, want context.Canceled", err)
	}
	if c.deviceName != "initial" || c.IsRunning() || opens != 0 {
		t.Fatalf("cancelled set changed stopped capture: name=%q running=%v opens=%d", c.deviceName, c.IsRunning(), opens)
	}
}

func TestCaptureSetDeviceRecoversFromDisconnectedPreviousDevice(t *testing.T) {
	c := NewCaptureWithDevice("disconnected mic")
	c.active = true // recovery already closed the disconnected device
	var opened string
	c.open = func() error { opened = c.deviceName; return nil }
	c.closeDevice = func() {}
	defer c.Stop()
	if err := c.SetDevice(context.Background(), "Studio Display Microphone"); err != nil {
		t.Fatal(err)
	}
	if !c.IsRunning() || opened != "Studio Display Microphone" {
		t.Fatalf("rebind did not open replacement: running=%v opened=%q", c.IsRunning(), opened)
	}
}
