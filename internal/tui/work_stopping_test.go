package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestJobRowShowsStoppingUntilTheWorkerReturns(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	step := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }
	step(events.JobStarted{ID: "job-1", Task: "audit the config"})
	step(events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"})
	step(events.JobAborted{ID: "job-1", Task: "audit the config", Elapsed: 4 * time.Second, Reason: events.AbortCancelled, Stopping: true})

	row := stripANSI(m.items[m.completedJobItems["job-1"]].sys)
	if !strings.Contains(row, "killed after 4s · stopping…") {
		t.Fatalf("row = %q, want the cancel marked stopping", row)
	}
	if m.jobStatus != "" {
		t.Fatalf("jobStatus = %q, want a stopping job off the chip", m.jobStatus)
	}

	step(events.JobExited{ID: "job-1", Task: "audit the config"})
	row = stripANSI(m.items[m.completedJobItems["job-1"]].sys)
	if strings.Contains(row, "stopping") || !strings.Contains(row, "killed after 4s") {
		t.Fatalf("row after the exit = %q, want the stopping mark gone", row)
	}
}

func TestJobRowWithoutStoppingIsSealedAtOnce(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.ready = true
	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "audit the config"}})
	m, _ = m.Update(busEventMsg{event: events.JobAborted{ID: "job-1", Task: "audit the config", Elapsed: 4 * time.Second}})
	if row := stripANSI(m.items[m.completedJobItems["job-1"]].sys); strings.Contains(row, "stopping") {
		t.Fatalf("row = %q, want no stopping mark for a worker that returned", row)
	}
}

// Against a real pipeline: stopping a task whose worker ignores its cancel
// returns to the TUI at once, the row says stopping, and the next refresh
// after the worker returns says it ended.
func TestWorkersStopStaysResponsiveWhileTheWorkerIsStopping(t *testing.T) {
	jobs := job.NewTable(t.Context())
	p := &pipeline.Pipeline{Events: events.NewBus()}
	pipeline.NewAgency(p, jobs, nil, &config.Config{}, nil).Wire()
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	id, err := jobs.Start(t.Context(), job.Spec{Task: "stubborn task", Run: func(context.Context) (string, error) {
		close(started)
		<-release
		return "late", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	m := newWorkers(&config.Config{}, p)
	m.width, m.height, m.cursor = 100, 24, 2
	updated := make(chan workersModel, 1)
	go func() { updated <- workersKey(m, "s") }()
	select {
	case m = <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("stopping a task blocked the TUI on the worker's exit")
	}
	if !strings.Contains(m.message, "requested") {
		t.Fatalf("message = %q", m.message)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "stubborn task · stopping") {
		t.Fatalf("view = %q, want the task shown stopping", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := stripANSI(m.View()); !strings.Contains(view, "Stopping…") || !strings.Contains(view, "Termination: unknown") {
		t.Fatalf("detail = %q, want stopping with termination unknown", view)
	}

	close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := jobs.AwaitExit(ctx, id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		m, _ = m.Update(workersTickMsg{m.token})
		view := stripANSI(m.View())
		if strings.Contains(view, "confirmed (worker returned)") && !strings.Contains(view, "Stopping…") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detail after the exit = %q, want the return confirmed", view)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
