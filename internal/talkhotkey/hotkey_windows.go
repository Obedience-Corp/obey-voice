// RegisterHotKey registration for the talk hotkey, and the only file in this
// package that touches Win32.
//
// RegisterHotKey is the Windows counterpart of the Carbon path: it swallows the
// combo so no other app sees it, and it needs no elevation or accessibility
// grant. It differs in two ways that shape everything below. It is exclusive —
// a second registration of the same combo fails with 1409 rather than firing
// twice, which is the one error worth explaining. And it reports only the
// press, never the release, so the hold duration is measured by polling the key
// itself.
//go:build windows

package talkhotkey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	// MOD_NOREPEAT: an autorepeating key must not look like a burst of
	// presses. The driver ignores repeats anyway; this stops them at the door.
	modNoRepeat uint32 = 0x4000

	wmQuit   uint32 = 0x0012
	wmHotkey uint32 = 0x0312

	// releasePoll is how often a held key is checked for release. 15 ms is
	// under one frame: fast enough that a release feels immediate, slow enough
	// that holding the key costs nothing measurable.
	releasePoll = 15 * time.Millisecond
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procGetAsyncKeyState   = user32.NewProc("GetAsyncKeyState")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

// winMessage is MSG (winuser.h). Only the message and wParam fields are read;
// the rest are here so GetMessageW writes into a struct of the right size.
type winMessage struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// Serve registers the combo and writes one JSON line per event to out until
// ctx is done. probe registers, reports, and returns immediately — enough to
// answer "is this combo free on this machine" without holding it.
func Serve(ctx context.Context, combo Combo, out io.Writer, probe bool) error {
	vk, ok := combo.WindowsVirtualKey()
	if !ok {
		return fmt.Errorf("talk hotkey %s: %q has no Windows virtual key", combo, combo.Key)
	}
	// A hotkey registered with a nil window belongs to the calling thread's
	// message queue, and only that thread's GetMessage ever sees it. Everything
	// from here to shutdown therefore runs on one pinned OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	lines := &lineWriter{out: bufio.NewWriter(out)}
	if err := registerHotKey(combo.WindowsModifiers()|modNoRepeat, vk); err != nil {
		code := errorCode(err)
		lines.write(Event{Kind: KindError, Code: code, Detail: registerDetailWindows(combo, code)})
		return fmt.Errorf("register talk hotkey %s: %w", combo, err)
	}
	defer unregisterHotKey()

	lines.write(Event{Kind: KindRegistered, Combo: combo.String()})
	if probe {
		return nil
	}
	return pump(ctx, lines, vk)
}

// pump runs the thread's message loop until ctx is done.
func pump(ctx context.Context, lines *lineWriter, vk uint32) error {
	stop := context.AfterFunc(ctx, quitPoster(currentThreadID()))
	defer stop()

	// Ordered so the release watchers are gone — and can no longer emit —
	// before the channel they emit on is closed.
	events, closeEvents := eventSink(lines)
	defer closeEvents()
	done := make(chan struct{})
	var watchers sync.WaitGroup
	defer func() {
		close(done)
		watchers.Wait()
	}()

	var pressed atomic.Bool
	var message winMessage
	for {
		result, err := getMessage(&message)
		switch {
		case result == -1:
			return fmt.Errorf("talk hotkey message loop: %w", err)
		case result == 0: // WM_QUIT: ctx is done.
			return nil
		case message.message != wmHotkey || uint32(message.wParam) != hotkeyID:
			continue
		case !pressed.CompareAndSwap(false, true):
			// MOD_NOREPEAT already suppresses repeats; a press that arrives
			// while one is outstanding is not a second press.
			continue
		}
		down := time.Now()
		emitEvent(events, Event{Kind: KindDown})
		watchers.Go(func() {
			if released := waitForRelease(vk, done); released {
				emitEvent(events, Event{Kind: KindUp, HeldMS: time.Since(down).Milliseconds()})
			}
			pressed.Store(false)
		})
	}
}

// eventSink drains events onto out on a goroutine of its own, so nothing
// marshals on the loop thread — the only thread that can see the hotkey. The
// returned func closes the channel and waits for the drain.
func eventSink(lines *lineWriter) (chan Event, func()) {
	events := make(chan Event, eventBuffer)
	var writer sync.WaitGroup
	writer.Go(func() {
		for event := range events {
			lines.write(event)
		}
	})
	return events, func() {
		close(events)
		writer.Wait()
	}
}

// waitForRelease polls the key until it comes up, because RegisterHotKey
// reports presses only. False means the helper is shutting down with the key
// still held, and no release happened to report.
func waitForRelease(vk uint32, done <-chan struct{}) bool {
	ticker := time.NewTicker(releasePoll)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return false
		case <-ticker.C:
			if !keyIsDown(vk) {
				return true
			}
		}
	}
}

func emitEvent(events chan<- Event, event Event) {
	select {
	case events <- event:
	default:
	}
}

func registerDetailWindows(combo Combo, code int) string {
	if code == ConflictCodeWindows {
		return fmt.Sprintf("another program holds %s — Windows gives a hotkey to one process only. Quit whichever holds it, or choose a different combo.", combo.Display())
	}
	return fmt.Sprintf("could not register %s (Windows error %d)", combo.Display(), code)
}

func registerHotKey(modifiers, vk uint32) error {
	result, _, err := procRegisterHotKey.Call(0, hotkeyID, uintptr(modifiers), uintptr(vk))
	if result == 0 {
		return err
	}
	return nil
}

func unregisterHotKey() {
	_, _, _ = procUnregisterHotKey.Call(0, hotkeyID)
}

// getMessage blocks until the thread has a message. It returns -1 on error and
// 0 on WM_QUIT, matching GetMessageW.
func getMessage(message *winMessage) (int32, error) {
	result, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(message)), 0, 0, 0)
	return int32(result), err
}

// quitPoster stops the loop from another goroutine, which is the only way to
// interrupt a thread parked in GetMessageW.
func quitPoster(threadID uintptr) func() {
	return func() {
		_, _, _ = procPostThreadMessageW.Call(threadID, uintptr(wmQuit), 0, 0)
	}
}

func currentThreadID() uintptr {
	id, _, _ := procGetCurrentThreadID.Call()
	return id
}

// keyIsDown reads the high bit of GetAsyncKeyState, which is set while the key
// is physically down.
func keyIsDown(vk uint32) bool {
	state, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return uint16(state)&0x8000 != 0
}

// errorCode is the Win32 error number behind a syscall failure, so the event
// carries the code a user can search for.
func errorCode(err error) int {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return int(errno)
	}
	return 0
}
