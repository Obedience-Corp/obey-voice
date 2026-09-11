package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
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

func TestSpeechDiagnosticOnlyPrintsVerifiedAction(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "Obey", &buf, true)

	// Routine measurements remain available to the transcript/activity log,
	// but do not become ordinary CLI chatter.
	bus.Emit(events.SpeechDiagnostic{
		Provider: "kokoro", Symptom: "synthesis_starvation", Cause: "unknown",
		Message: "routine measurement", Confidence: "low",
	})
	// An unknown/no-action observation is also neutral even if it has text.
	bus.Emit(events.SpeechDiagnostic{Cause: "unknown", Message: "insufficient evidence"})
	// A classifier-approved remedy follows the same Info output path exactly
	// once for the one emitted event.
	bus.Emit(events.SpeechDiagnostic{
		ActionCode: "kokoro_speech_recovery", Message: "Try the Kokoro fallback voice.",
	})
	// Existing real errors retain their current visible output.
	bus.Emit(events.Error{Stage: "tts", Message: "worker exited"})

	got := buf.String()
	if strings.Contains(got, "routine measurement") || strings.Contains(got, "insufficient evidence") {
		t.Fatalf("routine/unknown diagnostics leaked to CLI: %q", got)
	}
	if count := strings.Count(got, "Try the Kokoro fallback voice."); count != 1 {
		t.Fatalf("actionable diagnostic count = %d, want 1: %q", count, got)
	}
	if !strings.Contains(got, "Error: [tts] worker exited") {
		t.Fatalf("real error missing: %q", got)
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

func TestWorkStateChangedPrintsHeldResultOnceAndFencesOldRevision(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "agent", &buf, true)
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r1", ConversationEpoch: "c1"}
	e := events.WorkStateChanged{Snapshot: work.Snapshot{
		Scope: scope, Revision: 2,
		Pending: []work.ResultView{{State: work.Held, Outcome: work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Status: work.OutcomeSucceeded, Text: "sunny", PreviousSession: true}}},
	}}
	bus.Emit(e)
	bus.Emit(e)
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Pending: []work.ResultView{{State: work.Held, Outcome: work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Status: work.OutcomeSucceeded, Text: "stale"}}}}})
	got := buf.String()
	if strings.Count(got, "Previous session update: sunny") != 1 || strings.Contains(got, "stale") {
		t.Fatalf("work projection = %q", got)
	}
}

func TestWorkStateChangedCurrentHeldResultKeepsCurrentSessionLabel(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "agent", &buf, true)
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Pending: []work.ResultView{{State: work.Held, Outcome: work.Outcome{ResultID: "held", Scope: scope, Text: "held now"}}}}})
	got := buf.String()
	if !strings.Contains(got, "Background result (held): held now") || strings.Contains(got, "Previous session update: held now") {
		t.Fatalf("held output = %q", got)
	}
}

func TestCanonicalAdmissionAndResultAreKeyedWithoutLegacyDuplicates(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "agent", &buf, true)
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	job := work.JobView{Key: work.JobKey{Scope: scope, JobID: "job-1"}, Task: "inspect files", Origin: "spawn", State: work.JobQueued}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Jobs: []work.JobView{job}}})
	job.State = work.JobRunning
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 2, Jobs: []work.JobView{job}}})
	// Canonical mode suppresses the legacy lifecycle projection even if the
	// compatibility events arrive afterward.
	bus.Emit(events.JobQueued{ID: "job-1", Task: "inspect files"})
	bus.Emit(events.JobStarted{ID: "job-1", Task: "inspect files"})
	job.State = work.JobTerminal
	result := work.ResultView{State: work.Pending, DeliveryKey: job.Key, Outcome: work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Text: "done", Status: work.OutcomeSucceeded}}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 3, Jobs: []work.JobView{job}, Pending: []work.ResultView{result}}})
	bus.Emit(events.JobFinished{ID: "job-1", Task: "inspect files"})
	got := buf.String()
	if strings.Count(got, "job-1") != 2 || strings.Count(got, "Background result: done") != 1 {
		t.Fatalf("canonical projection = %q", got)
	}
	if !strings.Contains(got, "[spawn] running: inspect files") || !strings.Contains(got, "Background result: done") {
		t.Fatalf("canonical title/origin/result missing: %q", got)
	}
}

func TestCommunicatedWorkResultRemainsReadableAndDeduped(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "agent", &buf, true)
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	outcome := work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Text: "done", Status: work.OutcomeSucceeded}
	pending := work.ResultView{State: work.Ready, AnnouncementState: work.AnnouncementPending, Outcome: outcome, DeliveryKey: work.JobKey{Scope: scope, JobID: "job-1"}}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Pending: []work.ResultView{pending}}})
	pending.Task = "daily brief"
	pending.Outcome.Origin = "agency"
	pending.Outcome.Config = map[string]string{"persona": "Samantha"}
	pending.Outcome.SourceRef = "calendar"
	pending.Outcome.Limitations = "two items omitted"
	communicated := pending
	communicated.State = work.Communicated
	communicated.AnnouncementState = work.AnnouncementCommunicated
	communicated.LastAttempt = &work.DeliveryEvidence{AttemptID: "attempt-1", Mode: "text"}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 2, Communicated: []work.ResultView{communicated}}})
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 3, Communicated: []work.ResultView{communicated}}})
	got := buf.String()
	if strings.Count(got, "Background result: done") != 1 || strings.Count(got, "Background result (communicated): done") != 1 || !strings.Contains(got, "task: daily brief") || !strings.Contains(got, "persona: Samantha") || !strings.Contains(got, "source: calendar") || !strings.Contains(got, "limitations: two items omitted") || !strings.Contains(got, "attempt: attempt-1 (text)") {
		t.Fatalf("communicated projection = %q", got)
	}
}

func TestScopedToolEventsRejectDelayedOldScope(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	u := newUI(bus, "agent", &buf, true)
	old := work.WorkScope{SessionID: "s", RuntimeEpoch: "old", ConversationEpoch: "c"}
	current := work.WorkScope{SessionID: "s", RuntimeEpoch: "new", ConversationEpoch: "c"}
	u.SetWorkSnapshot(func() work.Snapshot { return work.Snapshot{Scope: current} })
	bus.Emit(events.ToolCallStarted{ID: "old", JobID: "job-old", Name: "read_file", Kind: "read", WorkScope: old})
	bus.Emit(events.ToolCallFinished{ID: "old", JobID: "job-old", Name: "read_file", Kind: "read", WorkScope: old})
	bus.Emit(events.ToolCallStarted{ID: "new", JobID: "job-new", Name: "read_file", Kind: "read", WorkScope: current})
	bus.Emit(events.ToolCallFinished{ID: "new", JobID: "job-new", Name: "read_file", Kind: "read", WorkScope: current})
	bus.Emit(events.ToolCallStarted{ID: "newer", JobID: "job-new", Name: "run_command", Kind: "execute", WorkScope: current})
	got := buf.String()
	if strings.Contains(got, "job-old") || !strings.Contains(got, "job-new") {
		t.Fatalf("scoped tool output = %q", got)
	}
}

func TestWorkStateChangedScopeRequiresClear(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	newUI(bus, "agent", &buf, true)
	first := work.WorkScope{SessionID: "s", RuntimeEpoch: "r1", ConversationEpoch: "c1"}
	second := work.WorkScope{SessionID: "s", RuntimeEpoch: "r2", ConversationEpoch: "c2"}
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: first, Revision: 1, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "old", Scope: first, Text: "old"}}}}})
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: second, Revision: 1, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "late", Scope: second, Text: "late"}}}}})
	bus.Emit(events.ConversationCleared{})
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: second, Revision: 1, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "new", Scope: second, Text: "new"}}}}})
	got := buf.String()
	if strings.Contains(got, "late") || !strings.Contains(got, "new") {
		t.Fatalf("scope projection = %q", got)
	}
}

func TestWorkStateChangedUsesAuthoritativeNewRuntimeScope(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	u := newUI(bus, "agent", &buf, true)
	first := work.WorkScope{SessionID: "s", RuntimeEpoch: "r1", ConversationEpoch: "c1"}
	second := work.WorkScope{SessionID: "s", RuntimeEpoch: "r2", ConversationEpoch: "c2"}
	authoritative := work.Snapshot{Scope: first}
	u.SetWorkSnapshot(func() work.Snapshot { return authoritative })
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: first, Revision: 1, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "old", Scope: first, Text: "old"}}}}})
	authoritative.Scope = second
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: second, Revision: 1, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "new", Scope: second, Text: "new"}}}}})
	// A delayed event from the retired runtime must not repaint the output.
	bus.Emit(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: first, Revision: 99, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "late", Scope: first, Text: "late"}}}}})
	got := buf.String()
	if !strings.Contains(got, "old") || !strings.Contains(got, "new") || strings.Contains(got, "late") {
		t.Fatalf("authoritative scope projection = %q", got)
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

func TestLiveToolOutcomeLinesUseObservedStatus(t *testing.T) {
	cases := []struct {
		name string
		e    events.ToolCallFinished
		want []string
	}{
		{name: "typed failed without legacy error", e: events.ToolCallFinished{
			Name: "run_command", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeFailed, ExitCode: intPtr(7), Reason: "process failed"},
		}, want: []string{"✗ run_command failed", "process failed", "exit code 7"}},
		{name: "unknown legacy completion", e: events.ToolCallFinished{Name: "read_file"}, want: []string{"? read_file outcome unknown"}},
		{name: "typed timeout", e: events.ToolCallFinished{
			Name: "run_command", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeTimedOut, Reason: "deadline exceeded"},
		}, want: []string{"✗ run_command timed out", "deadline exceeded"}},
		{name: "typed cancellation", e: events.ToolCallFinished{
			Name: "run_command", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeCanceled, Signal: "TERM"},
		}, want: []string{"✗ run_command canceled", "signal TERM"}},
		{name: "successful zero", e: events.ToolCallFinished{
			Name: "run_command", Err: "stale legacy error", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded, ExitCode: intPtr(0)},
		}, want: []string{"✓ run_command completed", "exit code 0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.NewBus()
			out := &bytes.Buffer{}
			newUI(bus, "Samantha", out, true)
			bus.Emit(tc.e)
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("stdout = %q, want %q", got, want)
				}
			}
			if tc.name == "successful zero" && strings.Contains(got, "stale legacy error") {
				t.Fatalf("typed outcome rendered stale legacy error: %q", got)
			}
		})
	}
}

func intPtr(n int) *int { return &n }

func concat(groups ...[]events.Event) []events.Event {
	var all []events.Event
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

func TestBudgetStopPrintsOverBudget(t *testing.T) {
	var buf bytes.Buffer
	bus := events.NewBus()
	NewWithOutput(bus, "agent", &buf)
	bus.Emit(events.JobStarted{ID: "job-1", Task: "audit"})
	bus.Emit(events.JobAborted{ID: "job-1", Task: "audit", Elapsed: 10 * time.Minute, Reason: events.AbortBudget})
	bus.Emit(events.JobAborted{ID: "job-2", Task: "tidy", Elapsed: 3 * time.Second, Reason: events.AbortCancelled})
	got := buf.String()
	for _, want := range []string{"job-1 over budget after 10m0s", "job-2 killed after 3s"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
