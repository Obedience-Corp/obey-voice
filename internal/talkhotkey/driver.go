package talkhotkey

import (
	"fmt"
	"strings"
	"time"
)

// Mode is how a key press maps onto a turn.
type Mode string

const (
	ModeHold      Mode = "hold"
	ModeToggle    Mode = "toggle"
	ModeAutomatic Mode = "automatic"

	DefaultMode = ModeHold
)

// Modes lists the modes in presentation order, for enum listings.
func Modes() []string {
	return []string{string(ModeHold), string(ModeToggle), string(ModeAutomatic)}
}

// ParseMode reads a mode name. Empty is the default rather than an error: an
// unset config key means "the shipped behavior", and only a wrong value is
// worth refusing.
func ParseMode(s string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return DefaultMode, nil
	case ModeHold, ModeToggle, ModeAutomatic:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported talk hotkey mode %q; want %s", s, strings.Join(Modes(), ", "))
	}
}

// TapThreshold splits a tap from a hold in automatic mode: a press released
// sooner than this was a tap, and the turn stays open until the next press.
const TapThreshold = 300 * time.Millisecond

// Action is what the conversation must do about a key event.
type Action int

const (
	ActionNone Action = iota
	ActionBegin
	ActionEnd
)

func (a Action) String() string {
	switch a {
	case ActionBegin:
		return "begin"
	case ActionEnd:
		return "end"
	default:
		return "none"
	}
}

// Driver turns hotkey down/up events into turn boundaries. It is the whole
// behavioral contract of the talk hotkey, kept free of I/O and of the clock so
// every rule below is a table test rather than a hardware run.
//
// Normative semantics (from the Mac app's HOTKEY.md):
//
//   - Key repeat: a Down while the key is already down is ignored, and an Up
//     with no matching Down is ignored. Only the first Down until its Up counts.
//   - Hold: Down begins the turn; Up ends it.
//   - Toggle: Down alternates — begin, then end. Up does nothing.
//   - Automatic: Down alternates like Toggle. Up ends the turn only when the
//     key was held at least TapThreshold; a shorter press was a tap, which
//     leaves the turn open for the next Down to close.
//   - TurnEnded: the conversation ended the turn itself (VAD, cancel). The turn
//     is no longer talking, but a key still physically down stays down, so its
//     coming Up ends nothing — the turn must never be ended twice.
type Driver struct {
	mode    Mode
	pressed bool
	talking bool
}

// NewDriver builds a driver in mode. An empty mode is the default.
func NewDriver(mode Mode) *Driver {
	if mode == "" {
		mode = DefaultMode
	}
	return &Driver{mode: mode}
}

// Mode is the mode this driver was built with.
func (d *Driver) Mode() Mode { return d.mode }

// Talking reports whether a turn is currently open.
func (d *Driver) Talking() bool { return d.talking }

// Down handles a key-down event.
func (d *Driver) Down() Action {
	if d.pressed {
		return ActionNone
	}
	d.pressed = true
	// Toggle and automatic both alternate on the press; only hold reserves the
	// end for the release.
	alternates := d.mode == ModeToggle || d.mode == ModeAutomatic
	if alternates && d.talking {
		d.talking = false
		return ActionEnd
	}
	d.talking = true
	return ActionBegin
}

// Up handles a key-up event. held is how long the key was down, measured by
// the helper that saw both edges.
func (d *Driver) Up(held time.Duration) Action {
	if !d.pressed {
		return ActionNone
	}
	d.pressed = false
	switch d.mode {
	case ModeToggle:
		return ActionNone
	case ModeAutomatic:
		if d.talking && held >= TapThreshold {
			d.talking = false
			return ActionEnd
		}
		return ActionNone
	default:
		if d.talking {
			d.talking = false
			return ActionEnd
		}
		return ActionNone
	}
}

// TurnEnded records that the conversation closed the turn on its own.
func (d *Driver) TurnEnded() { d.talking = false }

// Reset drops all state: the helper restarted, or the conversation was left,
// so nothing is known about the key or the turn any more.
func (d *Driver) Reset() {
	d.pressed = false
	d.talking = false
}
