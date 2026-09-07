package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestJobToolEventsLeaveTheLiveTurnAlone(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m.setStatus("Listening...", false)
	m.setVoiceMode(anim.ModeIdle)
	transcriptLines := len(m.transcript)

	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{
		ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read", Summary: "config.yaml",
	}})

	if !strings.Contains(m.jobStatus, "audit the config") || !strings.HasSuffix(m.jobStatus, "· read_file") {
		t.Fatalf("jobStatus = %q, want the task and the running tool", m.jobStatus)
	}
	if m.status != "Listening..." {
		t.Errorf("status = %q, want the live status line untouched", m.status)
	}
	if m.voiceMode != anim.ModeIdle {
		t.Errorf("voiceMode = %v, want the orb left idle by background work", m.voiceMode)
	}
	if len(m.transcript) != transcriptLines {
		t.Errorf("transcript grew by %d lines, want worker tools kept out of it", len(m.transcript)-transcriptLines)
	}

	m, _ = m.Update(busEventMsg{event: events.ToolCallFinished{
		ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read", Preview: "key: value",
		Elapsed: 40 * time.Millisecond,
	}})
	if m.status != "Listening..." || len(m.transcript) != transcriptLines {
		t.Errorf("finish changed status %q / transcript %d, want both untouched", m.status, len(m.transcript))
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

	if want := "Working: audit the config · read_file (+1 queued)"; m.jobStatus != want {
		t.Fatalf("chip while job-2 waits = %q, want %q", m.jobStatus, want)
	}

	step(events.JobFinished{ID: "job-1", Task: "audit the config", Elapsed: 2 * time.Second})
	if want := "Queued: write the report"; m.jobStatus != want {
		t.Fatalf("chip after job-1 finished = %q, want %q: job-2 is still waiting", m.jobStatus, want)
	}

	step(events.JobStarted{ID: "job-2", Task: "write the report"})
	if want := "Working: write the report"; m.jobStatus != want {
		t.Fatalf("chip once job-2 runs = %q, want %q", m.jobStatus, want)
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
	if want := "Working: job-4 · run_command"; m.jobStatus != want {
		t.Fatalf("chip = %q, want %q", m.jobStatus, want)
	}
}
