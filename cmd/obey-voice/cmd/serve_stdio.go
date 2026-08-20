//go:build !integration

package cmd

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// serveHumanOut receives all human-readable banner and log output. With
// --banner-json it is redirected to stderr so stdout carries only the
// machine-readable JSON event lines a supervisor parses; --log-file sends it
// to a file instead.
var serveHumanOut io.Writer = os.Stdout

// serveBannerOut receives the machine-readable JSON event lines. Always stdout
// in production; tests swap it to capture the exact wire bytes.
var serveBannerOut io.Writer = os.Stdout

// serveDiagOut receives the runtime diagnostics written outside the banner
// (TTS fallback warnings, per-turn library logs). It stays on stderr so the
// default console layout is byte-for-byte unchanged, and follows --log-file.
var serveDiagOut io.Writer = os.Stderr

// configureServeStdio prepares serve's output streams and must run before
// anything writes. It returns a restore function the caller always defers,
// even when it reports an error.
//
// A serve started by the macOS app outlives that app on purpose: quitting the
// app leaves the process running so the next launch adopts a warm brain. But
// quitting also closes the pipes serve was spawned with, and the first write to
// fd 1 or 2 that fails with EPIPE raises SIGPIPE, whose default disposition
// kills a Go process (os/signal). The orphan died silently on its next log
// line. Ignoring SIGPIPE turns those writes into ordinary EPIPE errors, which
// every writer below drops — no retry, and never a log about a failed log.
func configureServeStdio() (func(), error) {
	signal.Ignore(syscall.SIGPIPE)

	human, diag, logOut := serveHumanOut, serveDiagOut, log.Writer()
	restore := func() {
		serveHumanOut, serveDiagOut = human, diag
		log.SetOutput(logOut)
	}

	if serveBannerJSON {
		serveHumanOut = os.Stderr
	}
	path := strings.TrimSpace(serveLogFile)
	if path == "" {
		return restore, nil
	}

	file, err := openServeLogFile(path)
	if err != nil {
		return restore, err
	}
	serveHumanOut, serveDiagOut = file, file
	// The standard logger is the netapi meeting handlers' sink; it must follow
	// the same rule as everything else serve writes.
	log.SetOutput(file)
	// Every styled writer now targets a file. lipgloss only auto-detects the
	// console it was started against, so say it plainly: a log nobody reads on
	// a terminal should not be full of escape sequences.
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	return func() {
		lipgloss.SetColorProfile(profile)
		restore()
		_ = file.Close()
	}, nil
}

// openServeLogFile opens path for appending, creating it 0600. Writes reach
// the file unbuffered, so a `tail -f` sees each line as it is produced and a
// serve that is killed outright loses nothing.
func openServeLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open serve log file: %w", err)
	}
	return file, nil
}
