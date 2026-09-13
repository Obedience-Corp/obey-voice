package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/charmbracelet/lipgloss"
)

func stripSnapshot() work.Snapshot {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	out := work.Outcome{Scope: scope, JobID: "weather", ResultID: "forecast", Status: work.OutcomeSucceeded, Text: "Fixture forecast", Config: map[string]string{"persona": "samantha", "worker_tier": "simple"}}
	return work.Snapshot{Scope: scope, Revision: 1, Jobs: []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "weather"}, Task: "天気 weather", State: work.JobTerminal, Outcome: &out}}, Pending: []work.ResultView{{Outcome: out, Task: "Weather", State: work.Held, HoldReason: "muted", DeliveryKey: work.JobKey{Scope: scope, JobID: "weather"}}}}
}
func TestWorkStripRowsLayoutBoundaries(t *testing.T) {
	for _, tc := range []struct{ w, h, rows int }{{120, 28, 3}, {80, 20, 3}, {79, 20, 2}, {60, 20, 2}, {59, 20, 1}, {40, 20, 1}, {39, 20, 1}, {24, 12, 1}, {80, 19, 1}, {80, 11, 1}} {
		for _, vim := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d-vim%t", tc.w, tc.h, vim), func(t *testing.T) {
				m := sizedConversation(t, tc.w, tc.h)
				m.activityFocused = true
				m.vim.enabled = vim
				m.input.SetValue("draft")
				m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
				if m.workStripRows() != tc.rows {
					t.Fatalf("rows=%d want%d", m.workStripRows(), tc.rows)
				}
				strip := m.renderWorkStrip(time.Now())
				if strings.Count(strip, "\n") != tc.rows {
					t.Fatalf("actual strip rows=%q", strip)
				}
				for _, line := range strings.Split(strings.TrimSuffix(strip, "\n"), "\n") {
					if lipgloss.Width(line) > tc.w {
						t.Fatalf("strip overflow %q", line)
					}
				}
				view := stripANSI(m.View())
				if !strings.Contains(view, "Work ") || !strings.Contains(view, "draft") {
					t.Fatalf("status/input missing: %q", view)
				}
				if lipgloss.Height(view) > tc.h {
					t.Fatalf("height=%d exceeds%d: %q", lipgloss.Height(view), tc.h, view)
				}
			})
		}
	}
}
func TestSeenResultRemainsUncommunicatedAndDuplicateSafe(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.activityFocused = true
	snap := stripSnapshot()
	m.handleEvent(events.WorkStateChanged{Snapshot: snap})
	m.handleEvent(events.WorkStateChanged{Snapshot: snap})
	before := m.workCounts()
	if before.unseen != 1 || before.updates != 1 || before.held != 1 {
		t.Fatalf("counts=%+v", before)
	}
	m.workDisplay.seen["forecast"] = true
	after := m.workCounts()
	if after.unseen != 0 || after.updates != 1 || after.held != 1 || m.workStripRows() == 0 {
		t.Fatalf("seen result lost obligation: %+v", after)
	}
	if len(m.workResultItems) != 1 {
		t.Fatal("duplicate result card")
	}
}
func TestWorkSeenUsesWrappedVisibleRowsAndActivityDoesNotConsume(t *testing.T) {
	m := sizedConversation(t, 60, 20)
	m.appendTranscript(strings.Repeat("long history line\n", 40))
	m.activityFocused = true
	m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
	m.viewport.GotoTop()
	m.followChat = false
	m.markVisibleWorkResults()
	if m.workDisplay.seen["forecast"] {
		t.Fatal("Activity marked Chat result seen")
	}
	m.activityFocused = false
	m.markVisibleWorkResults()
	if m.workDisplay.seen["forecast"] {
		t.Fatal("offscreen result marked seen")
	}
	m.viewport.GotoBottom()
	m.markVisibleWorkResults()
	if !m.workDisplay.seen["forecast"] {
		t.Fatal("visible result was not marked seen")
	}
	if m.workCounts().updates != 1 {
		t.Fatal("visibility acknowledged delivery")
	}
}
func TestWorkStripCollapseAndNoFalseResponding(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	snap := stripSnapshot()
	snap.Pending = nil
	snap.Jobs[0].State = work.JobRunning
	m.observeWorkDisplay(snap)
	if m.workCounts().responding != 0 {
		t.Fatal("worker running shown as response")
	}
	result := stripSnapshot().Pending[0]
	result.State = work.Communicated
	result.AnnouncementState = work.AnnouncementCommunicated
	snap.Jobs = nil
	snap.Communicated = []work.ResultView{result}
	snap.Revision++
	m.observeWorkDisplay(snap)
	m.workDisplay.seen[result.Outcome.ResultID] = true
	if m.workStripRows() == 0 {
		t.Fatal("terminal summary collapsed immediately")
	}
	m.workDisplay.terminalUntil = time.Now().Add(-time.Second)
	if m.workStripRows() != 0 || m.renderWorkStrip(time.Now()) != "" {
		t.Fatal("completed seen strip did not collapse")
	}
}

func TestHiddenConversationDoesNotMarkResultSeen(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.workDisplayHidden = true
	m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
	if m.workDisplay.seen["forecast"] {
		t.Fatal("hidden conversation marked seen")
	}
	m.workDisplayHidden = false
	m.markVisibleWorkResults()
	if !m.workDisplay.seen["forecast"] {
		t.Fatal("visible conversation result not seen")
	}
}
