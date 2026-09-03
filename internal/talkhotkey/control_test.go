//go:build !windows

package talkhotkey

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlHome is a private XDG_RUNTIME_DIR for one test. It is created under
// /tmp rather than with t.TempDir because a unix socket path is capped near
// 104 bytes and the per-test temp directory is already most of that on macOS.
func controlHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "obey-talk")
	if err != nil {
		t.Fatalf("temp runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

func listenForTest(t *testing.T) *Control {
	t.Helper()
	control, err := ListenControl(t.Context())
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })
	return control
}

// nextEvent takes one event or fails; the endpoint answers on loopback, so a
// second of patience is already a hang.
func nextEvent(t *testing.T, control *Control) Event {
	t.Helper()
	select {
	case event, ok := <-control.Events():
		if !ok {
			t.Fatal("the event stream closed while events were still expected")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a control event")
		return Event{}
	}
}

// The three commands are the whole protocol: down opens, up closes and reports
// how long the key was held, press is both edges at once.
func TestControlRoundTrip(t *testing.T) {
	controlHome(t)
	control := listenForTest(t)

	if err := SendControl(t.Context(), "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	// Long enough that the held duration is a whole millisecond: a hold of
	// zero would read as a tap in automatic mode.
	time.Sleep(5 * time.Millisecond)
	if err := SendControl(t.Context(), "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := SendControl(t.Context(), "press"); err != nil {
		t.Fatalf("press: %v", err)
	}

	first := nextEvent(t, control)
	if first.Kind != KindDown || first.Proto != Proto {
		t.Fatalf("first event = %+v, want a %s tagged %s", first, KindDown, Proto)
	}
	second := nextEvent(t, control)
	if second.Kind != KindUp || second.HeldMS <= 0 {
		t.Fatalf("second event = %+v, want an %s with a measured hold", second, KindUp)
	}
	third, fourth := nextEvent(t, control), nextEvent(t, control)
	if third.Kind != KindDown || fourth.Kind != KindUp || fourth.HeldMS != 0 {
		t.Fatalf("press produced %+v then %+v, want a down and an up held 0", third, fourth)
	}
}

// A typo in a shortcut binding must be told off, and must not open a turn.
func TestControlRejectsUnknownCommands(t *testing.T) {
	controlHome(t)
	control := listenForTest(t)

	err := SendControl(t.Context(), "wiggle")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("SendControl(wiggle) = %v, want an unknown command error", err)
	}
	select {
	case event := <-control.Events():
		t.Fatalf("an unknown command emitted %+v", event)
	default:
	}
}

// The common case: the key was pressed before any conversation was started.
func TestSendControlWithoutAListener(t *testing.T) {
	controlHome(t)
	err := SendControl(t.Context(), "press")
	if !errors.Is(err, ErrNoListener) {
		t.Fatalf("SendControl with nothing listening = %v, want ErrNoListener", err)
	}
}

// Close is the whole teardown: the socket file goes, the stream closes, and
// the next conversation can take the endpoint.
func TestControlCloseReleasesTheEndpoint(t *testing.T) {
	dir := controlHome(t)
	control, err := ListenControl(t.Context())
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	path := filepath.Join(dir, "obey-voice", "talk.sock")
	if control.Address() != path {
		t.Fatalf("Address = %q, want %q", control.Address(), path)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after Close = %v, want the socket removed", err)
	}
	if _, ok := <-control.Events(); ok {
		t.Fatal("the event stream stayed open after Close")
	}
}

// A conversation that died leaves its socket file behind; the next one takes
// the endpoint rather than refusing to start over a file nothing answers.
func TestListenControlClearsAStaleSocket(t *testing.T) {
	dir := controlHome(t)
	path := filepath.Join(dir, "obey-voice", "talk.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("left behind"), 0o600); err != nil {
		t.Fatal(err)
	}

	first := listenForTest(t)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second := listenForTest(t)
	if err := SendControl(t.Context(), "press"); err != nil {
		t.Fatalf("press against the re-taken endpoint: %v", err)
	}
	if event := nextEvent(t, second); event.Kind != KindDown {
		t.Fatalf("first event = %+v, want %s", event, KindDown)
	}
}
