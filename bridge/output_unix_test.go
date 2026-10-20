//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"testing"
)

// Redirecting process descriptors must run in a subprocess so the test runner
// and concurrent tests retain their own stdout/stderr.
func TestProtocolOutputIsolatesNativeStdout(t *testing.T) {
	if os.Getenv("OBEY_TEST_PROTOCOL_OUTPUT") == "1" {
		out, err := protocolOutput()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := unix.Write(1, []byte("native stdout diagnostic\n")); err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, "Go stdout diagnostic")
		if err := json.NewEncoder(out).Encode(map[string]string{"event": "ready"}); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProtocolOutputIsolatesNativeStdout$")
	cmd.Env = append(os.Environ(), "OBEY_TEST_PROTOCOL_OUTPUT=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v: %s", err, stderr.String())
	}
	if stdout.String() != "{\"event\":\"ready\"}\n" {
		t.Fatalf("protocol contaminated: %q", stdout.String())
	}
	for _, line := range []string{"native stdout diagnostic", "Go stdout diagnostic"} {
		if !bytes.Contains(stderr.Bytes(), []byte(line)) {
			t.Fatalf("missing diagnostic %q: %s", line, stderr.String())
		}
	}
}
