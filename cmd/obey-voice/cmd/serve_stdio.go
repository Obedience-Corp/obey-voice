//go:build !integration

package cmd

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/sys/unix"
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
// (TTS fallback warnings, library setup logs). It stays on stderr so the
// default console layout is byte-for-byte unchanged, and --log-file moves the
// whole of fd 2 underneath it.
var serveDiagOut io.Writer = os.Stderr

// configureServeStdio prepares serve's output streams and must run before
// serve writes anything. It returns a restore function the caller always
// defers, even when it reports an error.
//
// A serve started by the macOS app outlives that app on purpose: quitting the
// app leaves the process running so the next launch adopts a warm brain. But
// quitting also closes the pipes serve was spawned with, and the first write to
// fd 1 or 2 that fails with EPIPE raises SIGPIPE, whose default disposition
// kills a Go process (os/signal). The orphan died silently on its next log
// line. dropBrokenPipeSignals turns those writes into ordinary EPIPE errors,
// which every writer below drops — no retry, and never a log about a failed
// log.
func configureServeStdio() (func(), error) {
	dropBrokenPipeSignals()

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
	// fd 2 itself moves to the file, so every stderr writer follows in one
	// place: the per-turn brain and pipeline diagnostics that hardcode
	// os.Stderr, cgo workers, and children that inherit the descriptor. fd 1
	// is deliberately left alone — it carries the --banner-json protocol.
	// serveHumanOut and serveDiagOut then address the same file through their
	// own handle; each line still has exactly one writer, and both handles are
	// O_APPEND so their writes cannot overwrite each other.
	restoreStderr, err := redirectStderrTo(file)
	if err != nil {
		_ = file.Close()
		return restore, err
	}
	serveHumanOut, serveDiagOut = file, file
	// The standard logger is the netapi meeting handlers' and http.Server's
	// sink; it must follow the same rule as everything else serve writes.
	log.SetOutput(file)
	// Every styled writer now targets a file. lipgloss only auto-detects the
	// console it was started against, so say it plainly: a log nobody reads on
	// a terminal should not be full of escape sequences.
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)

	return func() {
		restoreStderr()
		lipgloss.SetColorProfile(profile)
		restore()
		// The file is deliberately left open. serve's dispatcher, meeting
		// janitor and brain warmup are never joined, so closing it here would
		// be a write-after-close on a descriptor they still hold; process exit
		// releases it, and every write to it was unbuffered anyway.
	}, nil
}

// openServeLogFile opens path for appending, creating it 0600 and enforcing
// 0600 on a file that already existed — the banner writes the bearer token and
// the pairing code into it, and O_CREATE's mode applies only on creation.
// Writes reach the file unbuffered, so a `tail -f` sees each line as it is
// produced and a serve that is killed outright loses nothing.
func openServeLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open serve log file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict serve log file to 0600: %w", err)
	}
	return file, nil
}

// redirectStderrTo points fd 2 at file, returning a function that puts the
// original descriptor back.
func redirectStderrTo(file *os.File) (func(), error) {
	saved, err := unix.Dup(unix.Stderr)
	if err != nil {
		return nil, fmt.Errorf("save stderr: %w", err)
	}
	if err := unix.Dup2(int(file.Fd()), unix.Stderr); err != nil {
		_ = unix.Close(saved)
		return nil, fmt.Errorf("redirect stderr to the serve log file: %w", err)
	}
	return func() {
		_ = unix.Dup2(saved, unix.Stderr)
		_ = unix.Close(saved)
	}, nil
}

// serveHumanIsTerminal reports whether human output is going to a console.
// Progress animation and cursor control are only meaningful there; a file or a
// pipe gets whole lines instead.
func serveHumanIsTerminal() bool {
	file, ok := serveHumanOut.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
