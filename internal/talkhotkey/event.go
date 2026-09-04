package talkhotkey

import (
	"bytes"
	"encoding/json"
	"time"
)

// Proto tags every line the helper writes. The parent skips anything that does
// not carry it, so a stray log line from a library the helper links can never
// be read as a key event.
const Proto = "talk-hotkey/v1"

// Kind is what happened.
type Kind string

const (
	KindRegistered Kind = "registered"
	KindDown       Kind = "down"
	KindUp         Kind = "up"
	KindError      Kind = "error"
)

// Event is one line of the helper's stdout, JSON, newline-delimited.
type Event struct {
	Proto  string `json:"proto"`
	Kind   Kind   `json:"kind"`
	Combo  string `json:"combo,omitempty"`  // canonical spelling, on registered
	HeldMS int64  `json:"held_ms,omitzero"` // on up: milliseconds since the matching down
	Code   int    `json:"code,omitzero"`    // on error: the Carbon OSStatus
	Detail string `json:"detail,omitempty"` // on error: human text
}

// ConflictCode is Carbon's eventHotKeyExistsErr and ConflictCodeWindows is
// ERROR_HOTKEY_ALREADY_REGISTERED: some other process already owns this combo
// system-wide, so registration cannot succeed until it lets go. Two codes
// because the helper reports its platform's own status verbatim rather than
// translating it into a code no log or search would recognize.
const (
	ConflictCode        = -9878
	ConflictCodeWindows = 1409
)

// Conflict reports the one error the UI must explain rather than just print:
// the combo is taken, and the fix is to pick another one or quit the app
// holding it.
func (e Event) Conflict() bool {
	if e.Kind != KindError {
		return false
	}
	return e.Code == ConflictCode || e.Code == ConflictCodeWindows
}

// Held is how long the key was down, on an up event.
func (e Event) Held() time.Duration {
	return time.Duration(e.HeldMS) * time.Millisecond
}

// ParseEvent decodes one line. False means the line is not ours — blank,
// malformed, or from a different protocol version — and the parent skips it
// rather than treating unknown output as an error.
func ParseEvent(line []byte) (Event, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal(line, &event); err != nil {
		return Event{}, false
	}
	if event.Proto != Proto {
		return Event{}, false
	}
	return event, true
}

// Marshal encodes one line without its newline. It stamps Proto so no caller
// can emit an untagged event.
func (e Event) Marshal() ([]byte, error) {
	e.Proto = Proto
	return json.Marshal(e)
}
