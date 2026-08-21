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

// A console redraws frames in place; a file or pipe only accumulates them.
// The console bytes are pinned because serve's default path still writes them.
func TestUIFramingPerSink(t *testing.T) {
	cases := []struct {
		name  string
		plain bool
		want  string
	}{
		{
			name:  "console keeps cursor control and in-place frames",
			plain: false,
			want:  "  🎙 Listening...\r\x1b[2K  You: build status\n",
		},
		{
			name:  "plain sink drops the frame and keeps the record",
			plain: true,
			want:  "  You: build status\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			bus := events.NewBus()
			newUI(bus, "Samantha", &buf, tc.plain)

			bus.Emit(events.STTPhase{Phase: "listening"})
			bus.Emit(events.UserInput{Text: "build status"})

			if got := buf.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsConsole(t *testing.T) {
	cases := []struct {
		name string
		out  func(t *testing.T) io.Writer
		want bool
	}{
		{
			name: "a buffer is not a console",
			out:  func(*testing.T) io.Writer { return &bytes.Buffer{} },
			want: false,
		},
		{
			name: "a regular file is not a console",
			out: func(t *testing.T) io.Writer {
				file, err := os.CreateTemp(t.TempDir(), "log")
				if err != nil {
					t.Fatalf("create temp: %v", err)
				}
				t.Cleanup(func() { _ = file.Close() })
				return file
			},
			want: false,
		},
		{
			name: "a pipe is not a console",
			out: func(t *testing.T) io.Writer {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatalf("os.Pipe(): %v", err)
				}
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				return writer
			},
			want: false,
		},
		{
			name: "a character device is a console",
			out: func(t *testing.T) io.Writer {
				file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					t.Fatalf("open %s: %v", os.DevNull, err)
				}
				t.Cleanup(func() { _ = file.Close() })
				return file
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConsole(tc.out(t)); got != tc.want {
				t.Fatalf("isConsole() = %v, want %v", got, tc.want)
			}
		})
	}
}
