package talkhotkey

import (
	"testing"
	"time"
)

func TestEventRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Event
	}{
		{name: "registered", in: Event{Kind: KindRegistered, Combo: "alt+space"}},
		{name: "down", in: Event{Kind: KindDown}},
		{name: "up", in: Event{Kind: KindUp, HeldMS: 412}},
		{name: "error", in: Event{Kind: KindError, Code: ConflictCode, Detail: "already registered"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line, err := tc.in.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, b := range line {
				if b == '\n' {
					t.Fatalf("Marshal produced a multi-line record: %s", line)
				}
			}
			got, ok := ParseEvent(line)
			if !ok {
				t.Fatalf("ParseEvent rejected %s", line)
			}
			want := tc.in
			want.Proto = Proto
			if got != want {
				t.Errorf("round trip: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestParseEventRejects(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "blank", line: ""},
		{name: "whitespace", line: "   "},
		{name: "not json", line: "listening on :7263"},
		{name: "truncated json", line: `{"proto":"talk-hotkey/v1","kind":`},
		{name: "wrong proto", line: `{"proto":"talk-hotkey/v2","kind":"down"}`},
		{name: "no proto", line: `{"kind":"down"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if event, ok := ParseEvent([]byte(tc.line)); ok {
				t.Errorf("ParseEvent(%q) accepted it as %+v", tc.line, event)
			}
		})
	}
}

func TestEventConflictAndHeld(t *testing.T) {
	conflict := Event{Kind: KindError, Code: ConflictCode}
	if !conflict.Conflict() {
		t.Error("eventHotKeyExistsErr is not reported as a conflict")
	}
	for _, other := range []Event{
		{Kind: KindError, Code: -50},
		{Kind: KindDown},
		{Kind: KindRegistered, Code: ConflictCode},
	} {
		if other.Conflict() {
			t.Errorf("%+v reported as a conflict", other)
		}
	}
	if got := (Event{Kind: KindUp, HeldMS: 412}).Held(); got != 412*time.Millisecond {
		t.Errorf("Held() = %v, want 412ms", got)
	}
}
