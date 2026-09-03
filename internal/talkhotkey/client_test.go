package talkhotkey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

const (
	fakeHelperEnv  = "TALKHOTKEY_FAKE_HELPER"
	fakeHelperArgs = "TALKHOTKEY_FAKE_COMBO"
)

// TestFakeHelper is the helper process the Start tests drive: re-running this
// test binary with TALKHOTKEY_FAKE_HELPER set makes it print a scripted stdout
// instead of testing anything. That is the whole point — the client is only
// worth testing against a real child process with real pipes, and the Carbon
// helper cannot run in CI.
func TestFakeHelper(t *testing.T) {
	script := os.Getenv(fakeHelperEnv)
	if script == "" {
		t.Skip("helper-process entrypoint; driven by the Start tests")
	}
	emit := func(event Event) {
		line, err := event.Marshal()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(line))
	}
	emit(Event{Kind: KindRegistered, Combo: os.Getenv(fakeHelperArgs)})
	switch script {
	case "1":
		// Something that is not ours, between two events that are: the client
		// must skip it rather than stop reading or report it.
		fmt.Println("ggml_metal_init: found device")
		emit(Event{Kind: KindDown})
		emit(Event{Kind: KindUp, HeldMS: 412})
	case "block":
		// A helper that only leaves when it is told to.
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	os.Exit(0)
}

func fakeCommand(script string) Command {
	return func(ctx context.Context, combo Combo) (*exec.Cmd, error) {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFakeHelper$")
		cmd.Env = append(os.Environ(),
			fakeHelperEnv+"="+script,
			fakeHelperArgs+"="+combo.String())
		return cmd, nil
	}
}

func mustParse(t *testing.T, s string) Combo {
	t.Helper()
	combo, err := ParseCombo(s)
	if err != nil {
		t.Fatalf("ParseCombo(%q): %v", s, err)
	}
	return combo
}

func TestStartStreamsHelperEvents(t *testing.T) {
	combo := mustParse(t, "alt+space")
	client, err := Start(t.Context(), combo, Options{Command: fakeCommand("1")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Stop()

	var got []Event
	for event := range client.Events() {
		got = append(got, event)
	}
	want := []Event{
		{Proto: Proto, Kind: KindRegistered, Combo: "alt+space"},
		{Proto: Proto, Kind: KindDown},
		{Proto: Proto, Kind: KindUp, HeldMS: 412},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after the helper exited")
	}
}

func TestStopTerminatesAHelperBlockedOnStdin(t *testing.T) {
	combo := mustParse(t, "ctrl+alt+f13")
	client, err := Start(t.Context(), combo, Options{
		Command:     fakeCommand("block"),
		StopTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case event := <-client.Events():
		if event.Kind != KindRegistered || event.Combo != combo.String() {
			t.Fatalf("first event = %+v, want a registered %s", event, combo)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("helper never reported registration")
	}

	stopped := make(chan struct{})
	go func() {
		client.Stop()
		client.Stop() // idempotent, and every caller waits for the exit
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("Stop did not return")
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Stop returned before the helper exited")
	}
	if _, open := <-client.Events(); open {
		t.Error("Events is still open after the helper exited")
	}
}

func TestStartReturnsTheCommandError(t *testing.T) {
	want := errors.New("no talk-hotkey subcommand in this build")
	client, err := Start(t.Context(), mustParse(t, "alt+space"), Options{
		Command: func(context.Context, Combo) (*exec.Cmd, error) { return nil, want },
	})
	if err == nil {
		client.Stop()
		t.Fatal("Start succeeded with a failing command")
	}
	if !errors.Is(err, want) {
		t.Errorf("Start error = %v, want %v", err, want)
	}
}

func TestDefaultCommandSpawnsTheHiddenSubcommand(t *testing.T) {
	cmd, err := DefaultCommand(t.Context(), mustParse(t, "ctrl+alt+t"))
	if err != nil {
		t.Fatalf("DefaultCommand: %v", err)
	}
	want := []string{"talk-hotkey", "--combo", "ctrl+alt+t"}
	got := cmd.Args[1:]
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestSupportedFollowsThePlatform(t *testing.T) {
	if got, want := Supported(), runtime.GOOS == "darwin"; got != want {
		t.Errorf("Supported() = %v on %s", got, runtime.GOOS)
	}
}
