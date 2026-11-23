package tui

import (
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

func TestJobRowShowsSameOutputUntilTheJobEnds(t *testing.T) {
	m := sizedConversation(t, 140, 24)
	info := pipeline.WorkerInfo{ID: "job-1", Task: "list the project folder", State: "running"}
	m.deps.workers = func() []pipeline.WorkerInfo { return []pipeline.WorkerInfo{info} }
	m, _ = m.Update(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "list the project folder"}})
	row := func() string { return stripANSI(m.items[m.jobItems["job-1"]].sys) }
	if strings.Contains(row(), "same output") {
		t.Fatalf("row before any count: %q", row())
	}
	info.SameOutput = 7
	m, _ = m.Update(busEventMsg{event: events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "run_command", Kind: "execute", Summary: "ls -1"}})
	if !strings.Contains(strings.SplitN(row(), "\n", 2)[0], " · working · same output for 7 rounds") {
		t.Fatalf("row while repeating: %q", row())
	}
	idx := m.jobItems["job-1"]
	m, _ = m.Update(busEventMsg{event: events.JobFinished{ID: "job-1", Task: "list the project folder", Elapsed: 41 * time.Second, Summary: "Three files."}})
	if sealed := stripANSI(m.items[idx].sys); strings.Contains(sealed, "same output") || !strings.Contains(sealed, "done in 41s") {
		t.Fatalf("sealed row: %q", sealed)
	}
}
