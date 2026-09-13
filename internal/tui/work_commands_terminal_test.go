package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkCommandsTerminalShowsStripHeldResultAndPreservesDraft(t *testing.T) {
	m := sizedConversation(t, 100, 28)
	m.deps.runner = &fakeTurnRunner{}
	snap := stripSnapshot()
	h := &recoveryTerminal{model: m, views: make(chan string, 128)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&output), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	finished := false
	defer func() {
		if !finished {
			p.Quit()
			<-done
		}
	}()
	await := func(label string, predicate func(string) bool) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		last := ""
		for {
			select {
			case last = <-h.views:
				if predicate(last) {
					return
				}
			case <-timer.C:
				t.Fatalf("%s not rendered; last view:\n%s", label, last)
			}
		}
	}
	p.Send(busEventMsg{event: events.WorkStateChanged{Snapshot: snap}})
	await("held card and strip", func(v string) bool {
		return strings.Contains(v, "Background result (held)") && strings.Contains(v, "1 held")
	})
	sendTerminalEnter(p, "/work weather")
	await("selected work details", func(v string) bool {
		return strings.Contains(v, "Work · s · revision") && strings.Contains(v, "Origin persona: samantha")
	})
	sendTerminalText(p, "draft text")
	p.Send(tea.KeyMsg{Type: tea.KeyEsc})
	p.Send(tea.KeyMsg{Type: tea.KeyEsc})
	await("Chat with preserved draft", func(v string) bool {
		return strings.Contains(v, "draft text") && strings.Contains(v, "Background result (held)")
	})
	p.Quit()
	err := <-done
	finished = true
	if err != nil {
		t.Fatal(err)
	}
	if h.model.input.Value() != "draft text" || h.model.activityFocused || h.model.workFilter != "" {
		t.Fatalf("lost draft/focus: draft=%q activity=%v filter=%q", h.model.input.Value(), h.model.activityFocused, h.model.workFilter)
	}
	terminal := stripANSI(output.String())
	if !strings.Contains(terminal, "Background result (held)") || !strings.Contains(terminal, "1 held") {
		t.Fatalf("actual renderer omitted held state: %q", terminal)
	}
	if h.model.workCounts().updates != 1 {
		t.Fatal("terminal inspection consumed pending delivery")
	}
}
