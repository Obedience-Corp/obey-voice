package talkhotkey

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// macAppProcess is the Obey Voice Mac app's process name (PRODUCT_NAME "Obey
// Voice"). It registers the default combo for itself while it runs.
const macAppProcess = "Obey Voice"

// ownerProbeTimeout bounds the process lookup; pgrep answers in milliseconds.
const ownerProbeTimeout = time.Second

// ProcessRunning reports whether a process with exactly that name exists. A
// seam so tests never depend on what is running on the host.
var ProcessRunning = func(name string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownerProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "pgrep", "-x", name).Run() == nil
}

// OtherOwner names another Obey Voice client known to hold combo, so the
// caller can leave the key to it.
//
// This exists because registration cannot tell: macOS lets several processes
// register the same hot key and delivers each press to all of them (measured
// 2026-09-03 on macOS 26; no eventHotKeyExistsErr, both holders fired). A TUI
// and the Mac app both on ⌥Space would open two turns per press. The one
// collision that can be known is the Mac app, which registers the default
// combo whenever it runs.
func OtherOwner(combo Combo) (string, bool) {
	if !Supported() || combo.String() != DefaultCombo {
		return "", false
	}
	if ProcessRunning(macAppProcess) {
		return "the Obey Voice Mac app", true
	}
	return "", false
}
