package cmd

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

func runTalk(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newTalkCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

// A shortcut bound to the wrong word is a usage error, not a failed turn: exit
// 2 tells a script the binding is wrong rather than the conversation missing.
func TestTalkRejectsUnknownCommands(t *testing.T) {
	for _, args := range [][]string{{}, {"wiggle"}, {"down", "up"}} {
		err := runTalk(t, args...)
		if err == nil {
			t.Fatalf("talk %v = nil, want a usage error", args)
		}
		if code := ExitCode(err); code != 2 {
			t.Fatalf("talk %v exit code = %d, want 2 (%v)", args, code, err)
		}
	}
}

// The end-to-end shape: a live endpoint, one `obey-voice talk press`, both key
// edges on the conversation's stream.
func TestTalkPressReachesTheConversation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "obey-talk-cmd")
	if err != nil {
		t.Fatalf("temp runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)

	control, err := talkhotkey.ListenControl(t.Context())
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })

	if err := runTalk(t, "press"); err != nil {
		t.Fatalf("talk press: %v", err)
	}
	want := []talkhotkey.Kind{talkhotkey.KindDown, talkhotkey.KindUp}
	for _, kind := range want {
		select {
		case event := <-control.Events():
			if event.Kind != kind {
				t.Fatalf("event = %+v, want kind %s", event, kind)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// Nothing listening is exit 1 with the sentence a user can act on.
func TestTalkWithoutAConversationExitsOne(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "obey-talk-empty")
	if err != nil {
		t.Fatalf("temp runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)

	err = runTalk(t, "down")
	if !errors.Is(err, talkhotkey.ErrNoListener) {
		t.Fatalf("talk down with no conversation = %v, want ErrNoListener", err)
	}
	if code := ExitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// The command must not inherit the root's config load and prompt seeding: it
// runs on every keypress.
func TestTalkSkipsTheRootSetupHook(t *testing.T) {
	cmd := newTalkCmd()
	if cmd.PersistentPreRunE == nil {
		t.Fatal("talk has no PersistentPreRunE, so it inherits the root's config load")
	}
}
