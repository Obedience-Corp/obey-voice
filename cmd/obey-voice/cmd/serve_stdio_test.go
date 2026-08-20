//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// stdioChildEnv re-enters this test binary as the process under test: only a
// real child with a real fd 1 can prove what SIGPIPE does to it.
const stdioChildEnv = "OBEY_VOICE_TEST_STDIO_MODE"

// TestServeStdioClosedStdout is the defect: the macOS app spawns serve with
// pipes, quits, and the orphan it deliberately left running dies on its next
// write to a closed fd 1. The "raw" row is that behaviour; the "serve" row is
// the fix.
func TestServeStdioClosedStdout(t *testing.T) {
	if mode := os.Getenv(stdioChildEnv); mode != "" {
		writeUntilBrokenPipe(mode)
		return
	}

	cases := []struct {
		name       string
		mode       string
		wantSignal syscall.Signal
	}{
		{
			name:       "default disposition kills the writer",
			mode:       "raw",
			wantSignal: syscall.SIGPIPE,
		},
		{
			name:       "serve ignores SIGPIPE and survives",
			mode:       "serve",
			wantSignal: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runClosedStdoutChild(t, tc.mode)
			if tc.wantSignal == 0 {
				if err != nil {
					t.Fatalf("child exited with %v, want a clean exit: an orphaned serve must outlive its parent's pipes", err)
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child error = %v, want an ExitError carrying %v", err, tc.wantSignal)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != tc.wantSignal {
				t.Fatalf("child wait status = %v, want death by %v", exit.Sys(), tc.wantSignal)
			}
		})
	}
}

// runClosedStdoutChild starts this binary with a pipe for stdout, closes both
// ends, and waits: every write the child then makes to fd 1 fails with EPIPE.
func runClosedStdoutChild(t *testing.T, mode string) error {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}

	var stderr bytes.Buffer
	child := exec.Command(os.Args[0], "-test.run=^TestServeStdioClosedStdout$", "-test.timeout=60s")
	child.Env = append(os.Environ(), stdioChildEnv+"="+mode)
	child.Stdout = writer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// The child now owns the only write end; dropping both parent handles
	// leaves the pipe with no reader, exactly as a quit app does.
	_ = writer.Close()
	_ = reader.Close()

	err = child.Wait()
	if stderr.Len() > 0 {
		t.Logf("child stderr: %s", stderr.String())
	}
	return err
}

// writeUntilBrokenPipe runs in the child. It writes more than one pipe buffer
// so the closed reader is hit whether or not it lost the startup race, then
// exits explicitly rather than letting the test framework write to fd 1.
func writeUntilBrokenPipe(mode string) {
	if mode == "serve" {
		restore, err := configureServeStdio()
		if err != nil {
			fmt.Fprintf(os.Stderr, "configureServeStdio: %v\n", err)
			os.Exit(2)
		}
		defer restore()
	}
	line := strings.Repeat("x", 1024) + "\n"
	for range 256 {
		fmt.Fprint(serveHumanOut, line)
	}
	os.Exit(0)
}

// A failing write is dropped where it happens: the error is returned to the
// caller (never swallowed into a retry, never logged onto the same broken
// stream) and the process keeps running.
func TestServeHumanOutReturnsBrokenPipeError(t *testing.T) {
	restore, err := configureServeStdio()
	t.Cleanup(restore)
	if err != nil {
		t.Fatalf("configureServeStdio() error = %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err := reader.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}

	prev := serveHumanOut
	serveHumanOut = writer
	t.Cleanup(func() { serveHumanOut = prev })

	// Reaching the assertion at all is half the contract: an unignored SIGPIPE
	// would have killed this test process here.
	if _, err := fmt.Fprintln(serveHumanOut, "orphaned log line"); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("write error = %v, want EPIPE reported to the caller", err)
	}
}

func TestConfigureServeStdioLogFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("unopenable path is reported", func(t *testing.T) {
		withServeLogFile(t, filepath.Join(dir, "no-such-dir", "serve.log"))
		restore, err := configureServeStdio()
		t.Cleanup(restore)
		if err == nil {
			t.Fatal("configureServeStdio() error = nil, want the open failure surfaced")
		}
		if !strings.Contains(err.Error(), "open serve log file") {
			t.Fatalf("error = %v, want it to name the failed operation", err)
		}
	})

	t.Run("human and diagnostic output land in the file", func(t *testing.T) {
		path := filepath.Join(dir, "serve.log")
		if err := os.WriteFile(path, []byte("earlier run\n"), 0o600); err != nil {
			t.Fatalf("seed log: %v", err)
		}
		withServeLogFile(t, path)
		beforeHuman := serveHumanOut
		// A console profile, so the file proves styling is dropped rather than
		// having never been enabled in this environment.
		profile := lipgloss.ColorProfile()
		lipgloss.SetColorProfile(termenv.ANSI256)
		t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

		restore, err := configureServeStdio()
		if err != nil {
			t.Fatalf("configureServeStdio() error = %v", err)
		}
		fmt.Fprintln(serveHumanOut, dimStyle.Render("human line"))
		fmt.Fprintln(serveDiagOut, "diagnostic line")
		restore()

		if got := lipgloss.ColorProfile(); got != termenv.ANSI256 {
			t.Errorf("color profile = %v after restore, want the console profile back", got)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, want := range []string{"earlier run", "human line", "diagnostic line"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("log = %q, missing %q (the file is appended, never truncated)", data, want)
			}
		}
		if strings.Contains(string(data), "\x1b[") {
			t.Errorf("log = %q, want no terminal escape sequences in a file", data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat log: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
		if serveHumanOut != beforeHuman {
			t.Error("serveHumanOut was not restored after the log file closed")
		}
	})

	t.Run("absent flag leaves the default streams alone", func(t *testing.T) {
		withServeLogFile(t, "")
		beforeHuman, beforeDiag := serveHumanOut, serveDiagOut

		restore, err := configureServeStdio()
		t.Cleanup(restore)
		if err != nil {
			t.Fatalf("configureServeStdio() error = %v", err)
		}
		if serveHumanOut != beforeHuman {
			t.Errorf("serveHumanOut = %v, want the console stream untouched", serveHumanOut)
		}
		if serveDiagOut != beforeDiag {
			t.Errorf("serveDiagOut = %v, want the console stream untouched", serveDiagOut)
		}
	})
}

// Under --banner-json stdout is the caller's protocol channel: the human card
// (raw fmt.Print calls before this fix) must not reach it, so exactly one line
// arrives and it parses as the ready banner.
func TestServeStdoutCarriesOnlyTheBannerLine(t *testing.T) {
	personaEnv(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}

	var human bytes.Buffer
	origStdout, origHuman, origBanner := os.Stdout, serveHumanOut, serveBannerOut
	os.Stdout, serveHumanOut, serveBannerOut = writer, &human, writer
	t.Cleanup(func() {
		os.Stdout, serveHumanOut, serveBannerOut = origStdout, origHuman, origBanner
	})

	addr := "127.0.0.1:17262"
	creds := &netapi.Credentials{Token: "t0ken", Fingerprint: "fingerprint", TokenCreated: true}
	printServeBanner(addr, creds, &config.Config{})
	emitServeBannerJSON(addr, []string{addr}, creds)

	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout carried %d lines, want exactly the banner:\n%s", len(lines), data)
	}
	var ready netapi.ReadyBanner
	if err := json.Unmarshal([]byte(lines[0]), &ready); err != nil {
		t.Fatalf("decoding %q: %v", lines[0], err)
	}
	if ready.Event != "ready" {
		t.Errorf("event = %q, want ready", ready.Event)
	}
	if !strings.Contains(human.String(), "Use on another device") {
		t.Errorf("human output = %q, want the banner card routed to serveHumanOut", human.String())
	}
}

func withServeLogFile(t *testing.T, path string) {
	t.Helper()
	orig := serveLogFile
	serveLogFile = path
	t.Cleanup(func() { serveLogFile = orig })
}
