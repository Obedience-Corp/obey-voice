package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// jobLines returns the transcript lines belonging to background jobs, stripped
// of styling.
func jobLines(m conversationModel) []string {
	var lines []string
	for _, line := range m.transcript {
		if plain := stripANSI(line); strings.Contains(plain, "⚙") {
			lines = append(lines, plain)
		}
	}
	return lines
}

func TestJobToolEventsLeaveTheLiveTurnAlone(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m.setStatus("Listening...", false)
	m.setVoiceMode(anim.ModeIdle)
	items := len(m.items)

	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{
		ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read", Summary: "config.yaml",
	}})

	if !strings.Contains(m.jobStatus, "audit the config") || !strings.HasSuffix(m.jobStatus, "· read 1 file") {
		t.Fatalf("jobStatus = %q, want the task and the folded summary", m.jobStatus)
	}
	if m.status != "Listening..." {
		t.Errorf("status = %q, want the live status line untouched", m.status)
	}
	if m.voiceMode != anim.ModeIdle {
		t.Errorf("voiceMode = %v, want the orb left idle by background work", m.voiceMode)
	}
	if len(m.items) != items+1 {
		t.Errorf("thread grew by %d items, want exactly one for the job", len(m.items)-items)
	}

	m, _ = m.Update(busEventMsg{event: events.ToolCallFinished{
		ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read", Preview: "key: value",
		Elapsed: 40 * time.Millisecond,
	}})
	if m.status != "Listening..." || len(m.items) != items+1 {
		t.Errorf("finish changed status %q / thread %d, want both untouched", m.status, len(m.items))
	}
}

func TestJobRowsRewriteOneItemInPlace(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
	items := len(m.items)

	calls := []events.Event{
		events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"},
		events.ToolCallFinished{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"},
		events.ToolCallStarted{ID: "c2", JobID: "job-1", Name: "read_file", Kind: "read"},
	}
	for _, e := range calls {
		m, _ = m.Update(busEventMsg{event: e})
		if len(m.items) != items {
			t.Fatalf("thread grew to %d items on %T, want the job's one item rewritten", len(m.items), e)
		}
	}

	lines := jobLines(m)
	if len(lines) != 1 {
		t.Fatalf("job lines = %v, want exactly one", lines)
	}
	if !strings.Contains(lines[0], "job-1") || !strings.Contains(lines[0], "read 2 files") {
		t.Fatalf("job line = %q, want the folded rows", lines[0])
	}

	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{ID: "c3", JobID: "job-1", Name: "run_command", Kind: "execute"}})
	if lines = jobLines(m); len(lines) != 1 || !strings.Contains(lines[0], "read 2 files · ran 1 command") {
		t.Fatalf("job lines after a kind change = %v, want both rows on one line", lines)
	}
}

func TestJobEndSealsTheJobLine(t *testing.T) {
	cases := []struct {
		name  string
		event events.Event
		want  string
	}{
		{
			name:  "finished",
			event: events.JobFinished{ID: "job-1", Task: "audit the config", Elapsed: 35 * time.Second},
			want:  "done in 35s",
		},
		{
			name:  "failed",
			event: events.JobFailed{ID: "job-1", Task: "audit the config", Err: "worker exploded"},
			want:  "failed: worker exploded",
		},
		{
			name:  "aborted",
			event: events.JobAborted{ID: "job-1", Task: "audit the config", Elapsed: 6*time.Minute + 5*time.Second},
			want:  "killed after 6m5s",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedConversation(t, 120, 24)
			m.ready = true
			m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
			m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"}})
			items := len(m.items)

			m, _ = m.Update(busEventMsg{event: tc.event})

			if len(m.items) != items {
				t.Fatalf("thread grew to %d items on the end event, want the job's line sealed in place", len(m.items))
			}
			lines := jobLines(m)
			if len(lines) != 1 {
				t.Fatalf("job lines = %v, want exactly one", lines)
			}
			if !strings.Contains(lines[0], "read 1 file") || !strings.Contains(lines[0], tc.want) {
				t.Fatalf("sealed line = %q, want the rows and %q", lines[0], tc.want)
			}
			if m.jobStatus != "" {
				t.Errorf("jobStatus = %q after the job ended, want empty", m.jobStatus)
			}
		})
	}
}

func TestLiveToolEventsStillDriveStatus(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m.setStatus("Listening...", false)

	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{ID: "c1", Name: "list_files", Kind: "list_dir", Summary: "."}})

	if !strings.Contains(m.status, "list_files") {
		t.Fatalf("status = %q, want the live tool to own the status line", m.status)
	}
	if m.jobStatus != "" {
		t.Errorf("jobStatus = %q, want empty for a live tool call", m.jobStatus)
	}
	if lines := jobLines(m); len(lines) != 0 {
		t.Errorf("job lines = %v, want none for a live tool call", lines)
	}
}

func TestJobAbortedClearsTheChip(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
	if m.jobStatus == "" {
		t.Fatal("jobStatus empty after JobStarted")
	}

	m, _ = m.Update(busEventMsg{event: events.JobAborted{ID: "job-1", Task: "audit the config", Elapsed: 4200 * time.Millisecond}})

	if m.jobStatus != "" {
		t.Fatalf("jobStatus = %q after abort, want empty", m.jobStatus)
	}
	var details []string
	for _, e := range m.activity {
		details = append(details, e.stage+":"+e.detail)
	}
	if !strings.Contains(strings.Join(details, ","), "job:killed after 4s") {
		t.Fatalf("activity = %v, want a killed-after entry", details)
	}
}

// A spawn queued behind a running job must neither relabel the chip nor clear
// it when the running job ends: the chip names the running job, counts the
// queued one, then hands over once it actually starts.
func TestQueuedJobDoesNotRelabelTheRunningJob(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	step := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }

	step(events.JobQueued{ID: "job-1", Task: "audit the config"})
	step(events.JobStarted{ID: "job-1", Task: "audit the config"})
	step(events.JobQueued{ID: "job-2", Task: "write the report"})
	step(events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"})

	if want := "Working: audit the config · read 1 file (+1 queued)"; m.jobStatus != want {
		t.Fatalf("chip while job-2 waits = %q, want %q", m.jobStatus, want)
	}
	lines := jobLines(m)
	if len(lines) != 2 || !strings.Contains(lines[0], "job-1 · read 1 file") || !strings.Contains(lines[1], "job-2 · write the report · queued") {
		t.Fatalf("job lines while job-2 waits = %v, want job-1's rows and job-2 marked queued", lines)
	}

	step(events.JobFinished{ID: "job-1", Task: "audit the config", Elapsed: 2 * time.Second})
	if want := "Queued: write the report"; m.jobStatus != want {
		t.Fatalf("chip after job-1 finished = %q, want %q: job-2 is still waiting", m.jobStatus, want)
	}

	step(events.JobStarted{ID: "job-2", Task: "write the report"})
	if want := "Working: write the report"; m.jobStatus != want {
		t.Fatalf("chip once job-2 runs = %q, want %q", m.jobStatus, want)
	}
	if lines = jobLines(m); len(lines) != 2 || strings.Contains(lines[1], "queued") {
		t.Fatalf("job lines once job-2 runs = %v, want its line rewritten without the queued mark", lines)
	}

	step(events.JobFinished{ID: "job-2", Task: "write the report", Elapsed: time.Second})
	if m.jobStatus != "" {
		t.Fatalf("chip after both jobs = %q, want empty", m.jobStatus)
	}
}

// A tool event for a job the TUI never saw start (it attached mid-job) still
// gets a chip, named by the id.
func TestToolEventForUnknownJobStillShowsAChip(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{ID: "c1", JobID: "job-4", Name: "run_command", Kind: "execute"}})
	if want := "Working: job-4 · ran 1 command"; m.jobStatus != want {
		t.Fatalf("chip = %q, want %q", m.jobStatus, want)
	}
}
