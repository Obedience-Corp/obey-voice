//go:build !integration

package cmd

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// The standalone recorder runs its own Bubble Tea program and never redirects
// file descriptor 2, so a capture reopen line on stderr would be painted over
// the recorder during the mixer move it exists to recover from.
func TestMeetingCaptureLogfKeepsTheTUIPathOffStderr(t *testing.T) {
	dir := t.TempDir()
	if err := audio.SetDebugAudioDir(dir); err != nil {
		t.Fatalf("SetDebugAudioDir: %v", err)
	}
	t.Cleanup(func() { _ = audio.SetDebugAudioDir("") })

	logf, closeLog := meetingCaptureLogf(true)
	if logf == nil {
		t.Fatal("the TUI path must still record reopen diagnostics somewhere")
	}

	var stderr bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&stderr)
	t.Cleanup(func() { log.SetOutput(previous) })

	logf("capture from %q stalled; device reopened", "system default")
	closeLog()

	if stderr.Len() != 0 {
		t.Fatalf("TUI-path diagnostics reached the standard logger: %q", stderr.String())
	}
	got, err := os.ReadFile(appTUI.DiagnosticsLogPath())
	if err != nil {
		t.Fatalf("read diagnostics log: %v", err)
	}
	if !strings.Contains(string(got), "device reopened") {
		t.Fatalf("diagnostics log = %q, want the reopen line", got)
	}
}

func TestMeetingCaptureLogfUsesTheStandardLoggerWithoutTheTUI(t *testing.T) {
	logf, closeLog := meetingCaptureLogf(false)
	defer closeLog()
	if logf == nil {
		t.Fatal("a non-TUI run owns stderr and should keep logging there")
	}

	var stderr bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&stderr)
	t.Cleanup(func() { log.SetOutput(previous) })

	logf("capture from %q stalled; device reopened", "system default")
	if !strings.Contains(stderr.String(), "device reopened") {
		t.Fatalf("non-TUI diagnostics = %q, want the repo's standard logger", stderr.String())
	}
}
