//go:build !windows

package talkhotkey

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Windows shape — a loopback port plus a record on disk — driven on
// whatever host runs the tests. The record is the endpoint's name there, so
// every rule that protects a live unix socket has to protect it too, and none
// of that is testable if the path only runs on Windows.

// controlTCPHome forces the TCP endpoint and gives it a private record
// directory. It returns the record path.
func controlTCPHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	previous := useControlTCP
	useControlTCP = true
	t.Cleanup(func() { useControlTCP = previous })
	return filepath.Join(dir, controlDirName, controlInfoName)
}

func recordAt(t *testing.T, path string) controlInfoFile {
	t.Helper()
	info, err := readControlInfo(path)
	if err != nil {
		t.Fatalf("read the record at %s: %v", path, err)
	}
	return info
}

// The whole protocol over the token handshake, including the ping that a
// listener uses to decide whether a record it found is still alive.
func TestControlTCPRoundTrip(t *testing.T) {
	path := controlTCPHome(t)
	control := listenForTest(t)

	info := recordAt(t, path)
	if info.Address != control.Address() || info.Token == "" {
		t.Fatalf("record = %+v, want %s with a token", info, control.Address())
	}
	if err := SendControl(t.Context(), "ping"); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := SendControl(t.Context(), "press"); err != nil {
		t.Fatalf("press: %v", err)
	}
	if event := nextEvent(t, control); event.Kind != KindDown {
		t.Fatalf("first event = %+v, want %s", event, KindDown)
	}
	if event := nextEvent(t, control); event.Kind != KindUp {
		t.Fatalf("second event = %+v, want %s", event, KindUp)
	}
}

// A second conversation must not take the endpoint from the first. Overwriting
// the record would point every bound key at the newest TUI, and the first one
// out would then delete a record it no longer owns.
func TestListenControlTCPRefusesALiveEndpoint(t *testing.T) {
	path := controlTCPHome(t)
	first := listenForTest(t)

	second, err := ListenControl(t.Context())
	if err == nil {
		_ = second.Close()
		t.Fatal("a second listener took the endpoint from a live one")
	}
	if !strings.Contains(err.Error(), "already listening at "+first.Address()) {
		t.Fatalf("error = %v, want one naming %s", err, first.Address())
	}
	if info := recordAt(t, path); info.Address != first.Address() {
		t.Fatalf("record = %+v, want the live listener %s", info, first.Address())
	}
	// The refusal is not a broken endpoint: the first listener still answers.
	if err := SendControl(t.Context(), "ping"); err != nil {
		t.Fatalf("ping after the refusal: %v", err)
	}
}

// A conversation that died leaves its record behind. Nothing answers it, so
// the next conversation replaces it rather than refusing to start.
func TestListenControlTCPReplacesAStaleRecord(t *testing.T) {
	path := controlTCPHome(t)
	// Port 1 refuses, and a plain listener answers TCP without knowing the
	// protocol. Neither is a conversation, and both must be replaced.
	deaf, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = deaf.Close() })
	go func() {
		for {
			conn, err := deaf.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	for name, address := range map[string]string{"refused": "127.0.0.1:1", "not ours": deaf.Addr().String()} {
		t.Run(name, func(t *testing.T) {
			stale := controlInfoFile{Address: address, Token: "stale"}
			if err := writeControlInfo(path, stale); err != nil {
				t.Fatalf("write the stale record: %v", err)
			}
			control, err := ListenControl(t.Context())
			if err != nil {
				t.Fatalf("ListenControl over a stale record: %v", err)
			}
			defer func() { _ = control.Close() }()
			if info := recordAt(t, path); info.Address != control.Address() {
				t.Fatalf("record = %+v, want the new listener %s", info, control.Address())
			}
			if err := SendControl(t.Context(), "ping"); err != nil {
				t.Fatalf("ping after taking a stale endpoint: %v", err)
			}
		})
	}
}

// Close removes the record it wrote, so the next conversation starts clean.
func TestControlTCPCloseRemovesItsOwnRecord(t *testing.T) {
	path := controlTCPHome(t)
	control := listenForTest(t)
	if err := control.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after Close = %v, want the record removed", err)
	}
}

// Close must not remove a record that names somebody else. On Windows the
// cleanup used to delete the path unconditionally, so the first conversation
// out took the second one's endpoint with it.
func TestControlTCPCloseKeepsANewerRecord(t *testing.T) {
	path := controlTCPHome(t)
	control := listenForTest(t)

	newer := controlInfoFile{Address: "127.0.0.1:65000", Token: "somebody-else"}
	if err := writeControlInfo(path, newer); err != nil {
		t.Fatalf("write the newer record: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if info := recordAt(t, path); info != newer {
		t.Fatalf("record = %+v, want the newer listener's %+v", info, newer)
	}
}

// The dial probe has to answer in the time a listener is willing to wait for
// it; a slow one would stall every conversation start.
func TestControlInfoAnswersGivesUpQuickly(t *testing.T) {
	start := time.Now()
	if controlInfoAnswers(controlInfoFile{Address: "127.0.0.1:1"}) {
		t.Fatal("a refused port answered")
	}
	if elapsed := time.Since(start); elapsed > controlDialTimeout*4 {
		t.Fatalf("the probe took %s, want well under %s", elapsed, controlDialTimeout*4)
	}
}
