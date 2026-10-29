package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

func TestWorkerThreadScrollFollowsOnlyAtBottom(t *testing.T) {
	// A provider without message history must follow just like a full thread.
	controller := &testWorkers{items: []pipeline.WorkerInfo{{ID: "x", Task: "Inspect", State: "running"}}}
	for range 20 {
		controller.items[0].Tools = append(controller.items[0].Tools, workfold.Call{Name: "read_file", Summary: "file.txt", Status: "unknown"})
	}
	m := newWorkers(&config.Config{}, controller)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.openDetail("x")
	if !m.detail.AtBottom() {
		t.Fatal("newly opened thread did not show latest activity")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	controller.items[0].Tools = append(controller.items[0].Tools, workfold.Call{Name: "run_command", Summary: "new activity"})
	m, _ = m.Update(workersTickMsg{m.token})
	if m.detail.YOffset != 0 || !strings.Contains(stripANSI(m.detail.View()), "› Request") {
		t.Fatal("refresh pulled reader away from request")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	if m.detail.YOffset != 0 {
		t.Fatal("resize moved a scrolled reader")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	controller.items[0].Result, controller.items[0].State = "Final result", "done"
	m, _ = m.Update(workersTickMsg{m.token})
	if !m.detail.AtBottom() || !strings.Contains(stripANSI(m.detail.View()), "Final result") {
		t.Fatal("following reader missed completion")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !m.detail.AtBottom() {
		t.Fatal("resize lost following state")
	}
}

func TestWorkerThreadDetailToggleAndBoundedScreen(t *testing.T) {
	controller := &testWorkers{items: []pipeline.WorkerInfo{workerChatFixture()}}
	m := newWorkers(&config.Config{}, controller)
	for _, size := range [][2]int{{120, 30}, {80, 24}, {40, 12}, {20, 6}} {
		m, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.openDetail("chat-fixture")
		for range 2 {
			m = workersKey(m, "d")
			view := m.View()
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > size[0] {
					t.Fatalf("line overflow at %v: %q", size, line)
				}
			}
			if lipgloss.Height(view) > size[1] {
				t.Fatalf("screen overflow at %v", size)
			}
			if !strings.Contains(stripANSI(view), "esc") || !strings.Contains(stripANSI(view), "d details") && size[0] < 62 {
				t.Fatalf("narrow screen lost detail/navigation hints at %v", size)
			}
		}
		if m.expandTools || m.detailID != "chat-fixture" || len(controller.canceled) > 0 {
			t.Fatal("details key affected task ownership")
		}
	}
}
