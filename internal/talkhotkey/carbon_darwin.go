// Carbon registration for the talk hotkey, and the only file in this package
// that touches the platform.
//
// RegisterEventHotKey is used rather than a CGEventTap because it swallows the
// combo (no other app sees it), reports key-up as well as key-down, and needs
// no Accessibility permission — a tap would put a TCC prompt in front of the
// first spoken turn. Its cost is that the combo is exclusive system-wide, which
// is what a -9878 conflict means. Everything Carbon lives here so replacing it
// is one file.
//go:build darwin && cgo

package talkhotkey

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Carbon -framework Cocoa
#include <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>

extern void goTalkHotkeyEvent(UInt32 id, int pressed);
extern int goTalkHotkeyShouldQuit(void);

static OSStatus obeyTalkHotkeyHandler(EventHandlerCallRef next, EventRef event, void *userData) {
	EventHotKeyID hotkey;
	if (GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL,
	                      sizeof(hotkey), NULL, &hotkey) != noErr) {
		return eventNotHandledErr;
	}
	goTalkHotkeyEvent(hotkey.id, GetEventKind(event) == kEventHotKeyPressed ? 1 : 0);
	return noErr;
}

static OSStatus obeyInstallTalkHotkeyHandler(EventHandlerRef *handler) {
	EventTypeSpec events[2];
	events[0].eventClass = kEventClassKeyboard;
	events[0].eventKind = kEventHotKeyPressed;
	events[1].eventClass = kEventClassKeyboard;
	events[1].eventKind = kEventHotKeyReleased;
	return InstallApplicationEventHandler(NewEventHandlerUPP(obeyTalkHotkeyHandler), 2, events, NULL, handler);
}

static OSStatus obeyRegisterTalkHotkey(UInt32 keyCode, UInt32 modifiers, UInt32 id, EventHotKeyRef *ref) {
	EventHotKeyID hotkey;
	hotkey.signature = 0x4F424559; // 'OBEY', spelled in hex to dodge -Wmultichar
	hotkey.id = id;
	return RegisterEventHotKey(keyCode, modifiers, hotkey, GetApplicationEventTarget(), 0, ref);
}

// A registered hot key only fires inside an application run loop: on 64-bit
// macOS it is NSApplication that pulls key events off the window server and
// hands them to the Carbon handler (RunApplicationEventLoop is 32-bit only, and
// RunCurrentEventLoop pumps a queue nothing fills). So the helper is a faceless
// NSApplication — no Dock tile, no menu bar — exactly the shape the Mac app's
// own hotkey runs in. Shutdown happens on that same thread: a timer polls Go
// for the stop flag and stops the run loop from inside it.
static void obeyStopEventLoop(void) {
	[NSApp stop:nil];
	// -stop: takes effect after the next event; post one so the loop wakes.
	NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
	                                   location:NSZeroPoint
	                              modifierFlags:0
	                                  timestamp:0
	                               windowNumber:0
	                                    context:nil
	                                    subtype:0
	                                      data1:0
	                                      data2:0];
	[NSApp postEvent:wake atStart:YES];
}

static void obeyRunEventLoop(double pollSeconds) {
	@autoreleasepool {
		NSApplication *app = [NSApplication sharedApplication];
		[app setActivationPolicy:NSApplicationActivationPolicyProhibited];
		NSTimer *tick = [NSTimer scheduledTimerWithTimeInterval:pollSeconds
		                                                repeats:YES
		                                                  block:^(NSTimer *timer) {
			if (goTalkHotkeyShouldQuit()) {
				obeyStopEventLoop();
			}
		}];
		[app run];
		[tick invalidate];
	}
}
*/
import "C"

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// hotkeyID is our one registration. The handler is an application-wide one, so
// it must ignore ids it did not register.
const hotkeyID = 1

// stopPoll is how often the loop-thread timer checks whether Serve should
// quit: a quarter second of shutdown latency nobody can feel.
const stopPoll = 0.25

// session is the state one Serve call keeps about the physical key. The C
// callback cannot carry a Go pointer, so the live session is reachable through
// a package variable instead.
type session struct {
	mu        sync.Mutex
	pressed   bool
	pressedAt time.Time
	events    chan Event
	stop      atomic.Bool
}

var activeSession atomic.Pointer[session]

// Serve registers the combo and writes one JSON line per event to out until
// ctx is done. probe registers, reports, and returns immediately — enough to
// answer "is this combo available on this Mac" without holding it.
func Serve(ctx context.Context, combo Combo, out io.Writer, probe bool) error {
	keyCode, ok := combo.CarbonKeyCode()
	if !ok {
		return fmt.Errorf("talk hotkey %s: %q has no macOS key code", combo, combo.Key)
	}
	lines := &lineWriter{out: bufio.NewWriter(out)}

	var handler C.EventHandlerRef
	if status := C.obeyInstallTalkHotkeyHandler(&handler); status != C.noErr {
		lines.write(Event{Kind: KindError, Code: int(status), Detail: "could not install the keyboard event handler"})
		return fmt.Errorf("install talk hotkey handler: OSStatus %d", int(status))
	}
	defer C.RemoveEventHandler(handler)

	var ref C.EventHotKeyRef
	status := C.obeyRegisterTalkHotkey(C.UInt32(keyCode), C.UInt32(combo.CarbonModifiers()), hotkeyID, &ref)
	if status != C.noErr {
		lines.write(Event{Kind: KindError, Code: int(status), Detail: registerDetail(combo, int(status))})
		return fmt.Errorf("register talk hotkey %s: OSStatus %d", combo, int(status))
	}
	defer C.UnregisterEventHotKey(ref)

	lines.write(Event{Kind: KindRegistered, Combo: combo.String()})
	if probe {
		return nil
	}
	return watch(ctx, lines)
}

// watch runs the application event loop until ctx is done, draining the
// callback's events through one writer goroutine so nothing marshals on the
// loop thread.
func watch(ctx context.Context, lines *lineWriter) error {
	current := &session{events: make(chan Event, eventBuffer)}
	var writer sync.WaitGroup
	writer.Go(func() {
		for event := range current.events {
			lines.write(event)
		}
	})
	defer func() {
		close(current.events)
		writer.Wait()
	}()

	activeSession.Store(current)
	defer activeSession.Store(nil)
	unwatch := context.AfterFunc(ctx, func() { current.stop.Store(true) })
	defer unwatch()
	C.obeyRunEventLoop(C.double(stopPoll))
	return nil
}

//export goTalkHotkeyShouldQuit
func goTalkHotkeyShouldQuit() C.int {
	current := activeSession.Load()
	if current == nil || current.stop.Load() {
		return 1
	}
	return 0
}

//export goTalkHotkeyEvent
func goTalkHotkeyEvent(id C.UInt32, pressed C.int) {
	current := activeSession.Load()
	if current == nil || uint32(id) != hotkeyID {
		return
	}
	current.deliver(pressed != 0)
}

// deliver turns raw edges into one down and one up per physical press. A key
// held down can produce repeats; the parent must see the press once, and the
// hold duration is measured here because this is the only place that saw both
// edges of it.
func (s *session) deliver(pressed bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if pressed {
		if s.pressed {
			return
		}
		s.pressed = true
		s.pressedAt = now
		s.emit(Event{Kind: KindDown})
		return
	}
	if !s.pressed {
		return
	}
	s.pressed = false
	s.emit(Event{Kind: KindUp, HeldMS: now.Sub(s.pressedAt).Milliseconds()})
}

func (s *session) emit(event Event) {
	select {
	case s.events <- event:
	default:
	}
}

func registerDetail(combo Combo, status int) string {
	if status == ConflictCode {
		return fmt.Sprintf("%s is already registered by another app — the Obey Voice Mac app, Raycast, or another obey-voice TUI. Quit whichever holds it, or choose a different combo.", combo.Display())
	}
	return fmt.Sprintf("could not register %s (OSStatus %d)", combo.Display(), status)
}

type lineWriter struct{ out *bufio.Writer }

func (w *lineWriter) write(event Event) {
	line, err := event.Marshal()
	if err != nil {
		return
	}
	_, _ = w.out.Write(append(line, '\n'))
	_ = w.out.Flush()
}
