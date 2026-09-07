package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

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

func TestResponseStreamingStartedIsFirstToken(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "Samantha", &buf, true)

	bus.Emit(events.ResponseStreamingStarted{Elapsed: 1500 * time.Millisecond})
	got := buf.String()
	if !strings.Contains(got, "first token") {
		t.Fatalf("output = %q, want first token label", got)
	}
	if strings.Contains(got, "first response chunk") {
		t.Fatalf("output still uses the old first-response-chunk label: %q", got)
	}
}

func TestClockLabelsNeverSayPlaybackForNoSink(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "Obey", &buf, true)

	bus.Emit(events.ResponseStreamingStarted{Elapsed: time.Second})
	bus.Emit(events.VoiceGenerated{Elapsed: 2 * time.Second})
	bus.Emit(events.SpeakingComplete{Elapsed: 0, AudibleSink: "none"})

	got := buf.String()
	if strings.Contains(got, "playback") {
		t.Fatalf("no-sink output contains playback: %q", got)
	}
	if !strings.Contains(got, "first token") {
		t.Fatalf("missing first token label: %q", got)
	}
	if !strings.Contains(got, "synth first frame") {
		t.Fatalf("missing synth first frame label: %q", got)
	}
	if !strings.Contains(got, "synth drain (no sink)") {
		t.Fatalf("missing no-sink drain label: %q", got)
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

// Each job transition prints one dim line, so a queued job is visible as
// queued before it starts.
func TestJobTransitionsPrintOneLineEach(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	NewWithOutput(bus, "agent", &buf)

	bus.Emit(events.JobQueued{ID: "job-1", Task: "audit the config"})
	bus.Emit(events.JobStarted{ID: "job-1", Task: "audit the config"})
	bus.Emit(events.JobQueued{ID: "job-2", Task: "write the report"})
	bus.Emit(events.JobFinished{ID: "job-1", Task: "audit the config", Elapsed: 2 * time.Second})

	got := buf.String()
	for _, want := range []string{
		"job-1 queued: audit the config",
		"job-1 started: audit the config",
		"job-2 queued: write the report",
		"job-1 finished (2s)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "job-2 started") {
		t.Errorf("job-2 printed as started while only queued:\n%s", got)
	}
}
