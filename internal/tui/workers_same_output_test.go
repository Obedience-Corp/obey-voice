package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestWorkersListAndDetailShowSameOutput(t *testing.T) {
	controller := &testWorkers{items: []pipeline.WorkerInfo{
		{ID: "job-1", Task: "list the project folder", State: "running", Provider: "ollama", Model: "local-model", SameOutput: 12, Elapsed: 40 * time.Second},
		{ID: "job-2", Task: "watch the build", State: "running", Provider: "ollama", Model: "local-model", SameOutput: 2},
	}}
	m := newWorkers(&config.Config{}, controller)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})
	list := stripANSI(m.View())
	if strings.Count(list, "same output for") != 1 || !strings.Contains(list, "ollama / local-model · 40s · same output for 12 rounds") {
		t.Fatalf("list:\n%s", list)
	}
	m.cursor = 2
	m = workersKey(m, " ")
	if detail := stripANSI(m.View()); !strings.Contains(detail, "running · ollama / local-model · 40s · same output for 12 rounds") {
		t.Fatalf("detail:\n%s", detail)
	}

	controller.items[0].SameOutput = 0
	m, _ = m.Update(workersTickMsg{m.token})
	if strings.Contains(stripANSI(m.View()), "same output") {
		t.Fatal("a cleared count is still shown")
	}
}

// The worker stores the count after its round's last tool has finished, so
// the count event follows that tool's finish event.
func TestJobRowShowsSameOutputUntilTheJobEnds(t *testing.T) {
	m := sizedConversation(t, 140, 24)
	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "list the project folder"}})
	row := func() string { return strings.SplitN(stripANSI(m.items[m.jobItems["job-1"]].sys), "\n", 2)[0] }
	send := func(e events.Event) { m, _ = m.Update(busEventMsg{event: e}) }
	round := func(n int, rounds int) {
		id := fmt.Sprintf("c%d", n)
		send(events.ToolCallStarted{ID: id, JobID: "job-1", Name: "run_command", Kind: "execute", Summary: "ls -1"})
		send(events.ToolCallFinished{ID: id, JobID: "job-1", Name: "run_command", Kind: "execute"})
		if rounds >= 0 {
			send(events.WorkerSameOutput{JobID: "job-1", Rounds: rounds})
		}
	}
	round(1, -1)
	round(2, -1)
	round(3, 3)
	if !strings.HasSuffix(row(), " · working · same output for 3 rounds") {
		t.Fatalf("row after the third round: %q", row())
	}
	round(4, 4)
	if !strings.HasSuffix(row(), " · working · same output for 4 rounds") {
		t.Fatalf("row after the fourth round: %q", row())
	}
	round(5, 0)
	if strings.Contains(row(), "same output") {
		t.Fatalf("row after different output: %q", row())
	}
	send(events.WorkerSameOutput{JobID: "job-1", Rounds: 7})
	idx := m.jobItems["job-1"]
	send(events.JobFinished{ID: "job-1", Task: "list the project folder", Elapsed: 41 * time.Second, Summary: "Three files."})
	if sealed := stripANSI(m.items[idx].sys); strings.Contains(sealed, "same output") || !strings.Contains(sealed, "done in 41s") {
		t.Fatalf("sealed row: %q", sealed)
	}
	send(events.WorkerSameOutput{JobID: "job-1", Rounds: 8})
	if sealed := stripANSI(m.items[idx].sys); strings.Contains(sealed, "same output") {
		t.Fatalf("a late count reopened the sealed row: %q", sealed)
	}
}
