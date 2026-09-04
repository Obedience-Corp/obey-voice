//go:build !windows

package tui

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Quit runs on the update loop, so it may only ask the talk paths to end.
// Waiting for a helper to leave there froze the UI for the whole stop timeout
// every time somebody left a conversation — three seconds on macOS, and on
// Windows the full wait followed by a kill, because the interrupt it waited on
// is not deliverable to a child process there.
func TestQuitDoesNotWaitForTheTalkHelper(t *testing.T) {
	control, err := talkhotkey.ListenControl(t.Context())
	if err != nil {
		t.Skipf("no control endpoint on this host: %v", err)
	}
	// A helper that answers neither a closed stdin nor an interrupt, so the
	// only thing that could end it inside quit is the stop timeout.
	client, err := talkhotkey.Start(t.Context(), talkhotkey.Combo{}, talkhotkey.Options{
		StopTimeout: 30 * time.Second,
		Command: func(ctx context.Context, _ talkhotkey.Combo) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sh", "-c", `trap "" INT; sleep 30`), nil
		},
	})
	if err != nil {
		t.Fatalf("start the stubborn helper: %v", err)
	}

	app := App{cfg: &config.Config{}, runCtx: t.Context(), control: control, hotkey: client, width: 80, height: 24}
	start := time.Now()
	next, cmd := app.Update(quitMsg{})
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("quit took %s on the update loop, want it to ask and return", elapsed)
	}
	if cmd == nil {
		t.Fatal("quit scheduled nothing, want tea.Quit")
	}
	got := next.(App)
	if !got.quitting {
		t.Fatal("quit did not mark the app quitting")
	}
	// The endpoint is closed for real: it drops its connections first, so
	// there is nothing to wait for and nothing left listening.
	if got.control != nil {
		t.Fatal("quit left the control endpoint attached")
	}
	if _, ok := <-control.Events(); ok {
		t.Fatal("the control endpoint still answers after quit")
	}
	// The helper is still the teardown's problem; run() waits for it there.
	if got.hotkey == nil {
		t.Fatal("quit dropped the helper, so nothing would wait for it to exit")
	}
}
