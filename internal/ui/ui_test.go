package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// The event mirror is the busiest writer in a serve process. It must follow
// the sink it was given: under --banner-json stdout is the caller's protocol
// channel, and under --log-file the transcript belongs in the file.
func TestNewWithOutputWritesToTheGivenSink(t *testing.T) {
	cases := []struct {
		name string
		out  io.Writer
		want string
	}{
		{name: "nil falls back to stdout", out: nil, want: ""},
		{name: "explicit sink receives the event", out: &bytes.Buffer{}, want: "hello there"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.NewBus()
			u := NewWithOutput(bus, "Samantha", tc.out)
			if tc.out == nil {
				if u.out != io.Writer(os.Stdout) {
					t.Fatalf("out = %v, want stdout", u.out)
				}
				return
			}
			bus.Emit(events.UserInput{Text: "hello there"})
			got := tc.out.(*bytes.Buffer).String()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("sink = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
