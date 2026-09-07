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

// A worker's tool calls are folded on stdout the same way the TUI folds them:
// a row prints when its kind gives way to another, and when the job ends.
// Printing per call turned one background job into a wall of scrollback.
func TestWorkerToolLinesFoldIntoRows(t *testing.T) {
	call := func(id, name, kind string) []events.Event {
		return []events.Event{
			events.ToolCallStarted{ID: id, JobID: "job-1", Name: name, Kind: kind},
			events.ToolCallFinished{ID: id, JobID: "job-1", Name: name, Kind: kind},
		}
	}

	cases := []struct {
		name   string
		events []events.Event
		want   []string
		absent []string
	}{
		{
			name: "consecutive calls print nothing until the kind changes",
			events: concat(
				call("c1", "read_file", "read"),
				call("c2", "read_file", "read"),
			),
			absent: []string{"[job-1]"},
		},
		{
			name: "a kind change closes the row",
			events: concat(
				call("c1", "read_file", "read"),
				call("c2", "read_file", "read"),
				call("c3", "run_command", "execute"),
			),
			want:   []string{"[job-1] read 2 files"},
			absent: []string{"ran 1 command"},
		},
		{
			name: "the job end closes the last row before the job line",
			events: concat(
				call("c1", "read_file", "read"),
				[]events.Event{events.JobFinished{ID: "job-1", Task: "audit", Elapsed: 2 * time.Second}},
			),
			want: []string{"[job-1] read 1 file", "job-1 finished"},
		},
		{
			name: "a failed call is counted in its row",
			events: concat(
				[]events.Event{
					events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "run_command", Kind: "execute"},
					events.ToolCallFinished{ID: "c1", JobID: "job-1", Name: "run_command", Kind: "execute", Err: "error: boom"},
					events.JobFailed{ID: "job-1", Task: "audit", Err: "worker exploded"},
				},
			),
			want: []string{"[job-1] ran 1 command (1 failed)", "job-1 failed: worker exploded"},
		},
		{
			name:   "a live tool call still prints per call",
			events: []events.Event{events.ToolCallStarted{ID: "c1", Name: "read_file", Kind: "read", Summary: "a.go"}},
			want:   []string{"🔧 read_file (a.go)"},
			absent: []string{"[job-1]"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.NewBus()
			out := &bytes.Buffer{}
			newUI(bus, "Samantha", out, true)
			for _, e := range tc.events {
				bus.Emit(e)
			}
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("stdout = %q, want it to contain %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("stdout = %q, want it to omit %q", got, absent)
				}
			}
		})
	}
}

func concat(groups ...[]events.Event) []events.Event {
	var all []events.Event
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}
