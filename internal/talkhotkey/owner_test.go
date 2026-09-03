package talkhotkey

import "testing"

func withProcessRunning(t *testing.T, running map[string]bool) {
	t.Helper()
	prev := ProcessRunning
	ProcessRunning = func(name string) bool { return running[name] }
	t.Cleanup(func() { ProcessRunning = prev })
}

func TestOtherOwnerNamesTheMacAppOnTheDefaultCombo(t *testing.T) {
	if !Supported() {
		t.Skip("no helper on this platform")
	}
	withProcessRunning(t, map[string]bool{macAppProcess: true})
	combo, _ := ParseCombo(DefaultCombo)
	owner, ok := OtherOwner(combo)
	if !ok || owner != "the Obey Voice Mac app" {
		t.Fatalf("OtherOwner = %q, %v; want the Mac app", owner, ok)
	}
	// A different combo is not the app's business, running or not.
	other, _ := ParseCombo("ctrl+alt+space")
	if _, ok := OtherOwner(other); ok {
		t.Fatal("a non-default combo was attributed to the Mac app")
	}
}

func TestOtherOwnerIsSilentWhenTheAppIsNotRunning(t *testing.T) {
	withProcessRunning(t, map[string]bool{})
	combo, _ := ParseCombo(DefaultCombo)
	if owner, ok := OtherOwner(combo); ok {
		t.Fatalf("OtherOwner = %q with nothing running", owner)
	}
}
