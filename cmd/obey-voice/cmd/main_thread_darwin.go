//go:build darwin && cgo && !integration

package cmd

import "runtime"

// Carbon dispatches hotkey events on the main thread's event loop, so
// `talk-hotkey` must call RunCurrentEventLoop from the thread the process
// started on. Go documents that package initialization runs on the main
// goroutine and that LockOSThread there pins it to the main OS thread for the
// life of the program, which is the only supported way to guarantee that.
//
// It costs the rest of the binary nothing: only the main goroutine is pinned,
// every other goroutine still schedules freely, and the TUI does its terminal
// work off the main goroutine anyway.
func init() { runtime.LockOSThread() }
