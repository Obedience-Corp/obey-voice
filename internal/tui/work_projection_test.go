package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func projectionScope(runtime, conversation string) work.WorkScope {
	return work.WorkScope{SessionID: "session-1", RuntimeEpoch: runtime, ConversationEpoch: conversation}
}

func projectionSnapshot(scope work.WorkScope, state work.PendingState, previous bool, text string) work.Snapshot {
	return work.Snapshot{
		Scope: scope,
		Jobs: []work.JobView{{
			Key: scopeJob(scope, "job-1"), State: work.JobTerminal,
			Outcome: &work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Status: work.OutcomeSucceeded, Text: text, PreviousSession: previous},
		}},
		Pending: []work.ResultView{{
			Outcome: work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Status: work.OutcomeSucceeded, Text: text, PreviousSession: previous},
			State:   state, DeliveryKey: work.JobKey{Scope: scope, JobID: "job-1"},
		}},
	}
}

func scopeJob(scope work.WorkScope, id string) work.JobKey {
	return work.JobKey{Scope: scope, JobID: id}
}

func TestHeldWorkProjectionIsVisibleWhileOutputMuted(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.outputMuted = true
	scope := projectionScope("runtime-1", "conversation-1")
	m.handleEvent(events.WorkStateChanged{Snapshot: projectionSnapshot(scope, work.Held, true, "The weather check is ready.")})

	view := stripANSI(m.View())
	if !strings.Contains(view, "Previous session update: The weather check is ready.") {
		t.Fatalf("visible work projection = %q", view)
	}
	if len(m.workResultItems) != 1 {
		t.Fatalf("work result items = %d, want 1", len(m.workResultItems))
	}
}

func TestCurrentHeldWorkIsNotCalledPreviousSession(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	scope := projectionScope("runtime-1", "conversation-1")
	m.handleEvent(events.WorkStateChanged{Snapshot: projectionSnapshot(scope, work.Held, false, "held now")})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Background result (held): held now") || strings.Contains(view, "Previous session update: held now") {
		t.Fatalf("held projection = %q", view)
	}
}

func TestWorkProjectionReplacesJobFinishedLine(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	scope := projectionScope("runtime-1", "conversation-1")
	m.handleEvent(events.JobFinished{ID: "job-1", Task: "weather", Summary: "sunny"})
	m.handleEvent(events.WorkStateChanged{Snapshot: projectionSnapshot(scope, work.Ready, false, "Sunny and clear.")})

	view := stripANSI(m.View())
	if strings.Count(view, "Sunny and clear.") != 1 || strings.Contains(view, "done in") {
		t.Fatalf("job/result projection duplicated: %q", view)
	}
}

func TestWorkProjectionClearsOnScopeTransition(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	first := projectionScope("runtime-1", "conversation-1")
	next := projectionScope("runtime-1", "conversation-2")
	m.handleEvent(events.WorkStateChanged{Snapshot: projectionSnapshot(first, work.Ready, false, "old result")})
	m.handleEvent(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: next}})

	view := stripANSI(m.View())
	if strings.Contains(view, "old result") || len(m.workResultItems) != 0 {
		t.Fatalf("stale work projection survived scope transition: %q", view)
	}
}
