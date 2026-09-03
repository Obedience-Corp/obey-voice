package talkhotkey

import (
	"strings"
	"testing"
	"time"
)

// step is one event fed to the driver. held only matters for an up.
type step struct {
	event string // "down", "up", "turnEnded", "reset"
	held  time.Duration
	want  Action
}

func TestDriver(t *testing.T) {
	tests := []struct {
		name  string
		mode  Mode
		steps []step
	}{
		{
			name: "hold begins on press and ends on release",
			mode: ModeHold,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 900 * time.Millisecond, want: ActionEnd},
			},
		},
		{
			name: "hold ignores key repeat",
			mode: ModeHold,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "down", want: ActionNone},
				{event: "down", want: ActionNone},
				{event: "down", want: ActionNone},
				{event: "up", held: time.Second, want: ActionEnd},
			},
		},
		{
			name: "hold does not end a turn the conversation already ended",
			mode: ModeHold,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "turnEnded"},
				{event: "up", held: 2 * time.Second, want: ActionNone},
				{event: "down", want: ActionBegin},
				{event: "up", held: time.Second, want: ActionEnd},
			},
		},
		{
			name: "toggle alternates on presses and ignores releases",
			mode: ModeToggle,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 50 * time.Millisecond, want: ActionNone},
				{event: "down", want: ActionEnd},
				{event: "up", held: 50 * time.Millisecond, want: ActionNone},
				{event: "down", want: ActionBegin},
			},
		},
		{
			name: "toggle ignores a long hold",
			mode: ModeToggle,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 5 * time.Second, want: ActionNone},
			},
		},
		{
			name: "automatic treats a short press as a tap",
			mode: ModeAutomatic,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 299 * time.Millisecond, want: ActionNone},
				{event: "down", want: ActionEnd},
			},
		},
		{
			name: "automatic treats a long press as a hold",
			mode: ModeAutomatic,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 301 * time.Millisecond, want: ActionEnd},
				{event: "down", want: ActionBegin},
			},
		},
		{
			name: "automatic ends at exactly the threshold",
			mode: ModeAutomatic,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: TapThreshold, want: ActionEnd},
			},
		},
		{
			name: "automatic ignores repeats while held",
			mode: ModeAutomatic,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "down", want: ActionNone},
				{event: "up", held: time.Second, want: ActionEnd},
			},
		},
		{
			name: "an up with no down does nothing",
			mode: ModeHold,
			steps: []step{
				{event: "up", held: time.Second, want: ActionNone},
				{event: "down", want: ActionBegin},
				{event: "up", held: time.Second, want: ActionEnd},
				{event: "up", held: time.Second, want: ActionNone},
			},
		},
		{
			name: "reset forgets a held key and an open turn",
			mode: ModeHold,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "reset"},
				{event: "up", held: time.Second, want: ActionNone},
				{event: "down", want: ActionBegin},
			},
		},
		{
			name: "reset clears a toggled-on turn",
			mode: ModeToggle,
			steps: []step{
				{event: "down", want: ActionBegin},
				{event: "up", held: 50 * time.Millisecond, want: ActionNone},
				{event: "reset"},
				{event: "down", want: ActionBegin},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			driver := NewDriver(tc.mode)
			if driver.Mode() != tc.mode {
				t.Fatalf("Mode() = %q, want %q", driver.Mode(), tc.mode)
			}
			for i, s := range tc.steps {
				var got Action
				switch s.event {
				case "down":
					got = driver.Down()
				case "up":
					got = driver.Up(s.held)
				case "turnEnded":
					driver.TurnEnded()
				case "reset":
					driver.Reset()
				default:
					t.Fatalf("step %d: unknown event %q", i, s.event)
				}
				if got != s.want {
					t.Fatalf("step %d (%s %v) = %s, want %s", i, s.event, s.held, got, s.want)
				}
			}
		})
	}
}

func TestDriverTalkingTracksTheTurn(t *testing.T) {
	driver := NewDriver(ModeToggle)
	if driver.Talking() {
		t.Fatal("a fresh driver is talking")
	}
	driver.Down()
	if !driver.Talking() {
		t.Fatal("not talking after begin")
	}
	driver.TurnEnded()
	if driver.Talking() {
		t.Fatal("still talking after the conversation ended the turn")
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{in: "", want: DefaultMode},
		{in: "  ", want: DefaultMode},
		{in: "hold", want: ModeHold},
		{in: "HOLD", want: ModeHold},
		{in: " Toggle ", want: ModeToggle},
		{in: "automatic", want: ModeAutomatic},
		{in: "auto", wantErr: true},
		{in: "push-to-talk", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseMode(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseMode(%q) = %q, want an error", tc.in, got)
				}
				if !strings.Contains(err.Error(), "hold, toggle, automatic") {
					t.Errorf("error %q does not list the modes", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModesAreParseable(t *testing.T) {
	modes := Modes()
	if strings.Join(modes, ",") != "hold,toggle,automatic" {
		t.Fatalf("Modes() = %v, want hold, toggle, automatic in that order", modes)
	}
	for _, name := range modes {
		if _, err := ParseMode(name); err != nil {
			t.Errorf("Modes() lists %q, which does not parse: %v", name, err)
		}
	}
}

func TestActionString(t *testing.T) {
	for action, want := range map[Action]string{
		ActionNone:  "none",
		ActionBegin: "begin",
		ActionEnd:   "end",
	} {
		if got := action.String(); got != want {
			t.Errorf("Action(%d).String() = %q, want %q", action, got, want)
		}
	}
}
