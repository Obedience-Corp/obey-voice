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

func TestCanonicalAdmissionCardUpdatesAndReplacesWithResult(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	scope := projectionScope("runtime-1", "conversation-1")
	job := work.JobView{Key: scopeJob(scope, "job-1"), Task: "read the weather", Origin: "spawn"}
	m.handleEvent(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Jobs: []work.JobView{{Key: job.Key, Task: job.Task, Origin: job.Origin, State: work.JobQueued}}}})
	view := stripANSI(m.View())
	if strings.Count(view, "job-1") != 1 || !strings.Contains(view, "read the weather") {
		t.Fatalf("queued canonical card = %q", view)
	}
	if m.jobs["job-1"].origin != "spawn" {
		t.Fatalf("canonical origin = %q, want spawn", m.jobs["job-1"].origin)
	}
	m.handleEvent(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 2, Jobs: []work.JobView{{Key: job.Key, Task: job.Task, Origin: job.Origin, State: work.JobRunning}}}})
	view = stripANSI(m.View())
	if strings.Count(view, "job-1") != 1 || !strings.Contains(view, "Workers: 1 running") || !strings.Contains(view, "read the weather") {
		t.Fatalf("running canonical card = %q", view)
	}
	result := projectionSnapshot(scope, work.Ready, false, "Rain later")
	result.Revision = 3
	result.Pending[0].Task = "read the weather"
	result.Pending[0].Outcome.Origin = "spawn"
	result.Pending[0].Outcome.Config = map[string]string{"persona": "Samantha"}
	result.Jobs = []work.JobView{{Key: job.Key, Task: job.Task, Origin: job.Origin, State: work.JobTerminal, Outcome: &result.Pending[0].Outcome}}
	m.handleEvent(events.WorkStateChanged{Snapshot: result})
	view = stripANSI(m.View())
	if strings.Count(view, "Rain later") != 1 || !strings.Contains(view, "task: read the weather") || !strings.Contains(view, "persona: Samantha") || strings.Contains(view, "queued") || strings.Contains(view, "Working:") {
		t.Fatalf("result card did not replace admission card: %q", view)
	}
}

func TestCanonicalAdmissionDedupesLegacyLifecycleLine(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	scope := projectionScope("runtime-1", "conversation-1")
	m.handleEvent(events.JobQueued{ID: "job-1", Task: "inspect files"})
	m.handleEvent(events.WorkStateChanged{Snapshot: work.Snapshot{Scope: scope, Revision: 1, Jobs: []work.JobView{{Key: scopeJob(scope, "job-1"), Task: "inspect files", Origin: "spawn", State: work.JobQueued}}}})
	view := stripANSI(m.View())
	if strings.Count(view, "job-1") != 1 {
		t.Fatalf("canonical/legacy admission duplicated card: %q", view)
	}
}

func TestWorkProjectionRetainsCommunicatedResultAndShowsHoldReason(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	scope := projectionScope("runtime-1", "conversation-1")
	held := projectionSnapshot(scope, work.Held, false, "waiting")
	held.Revision = 1
	held.Pending[0].AnnouncementState = work.AnnouncementHeld
	held.Pending[0].HoldReason = "tts_failed"
	held.Pending[0].Task = "weather check"
	m.handleEvent(events.WorkStateChanged{Snapshot: held})
	if got := stripANSI(m.View()); !strings.Contains(got, "Background result (held): waiting (tts_failed)") {
		t.Fatalf("held result = %q", got)
	}
	communicated := work.Snapshot{Scope: scope, Revision: 2, Communicated: []work.ResultView{{
		State: work.Communicated, AnnouncementState: work.AnnouncementCommunicated,
		Outcome: held.Pending[0].Outcome, DeliveryKey: held.Pending[0].DeliveryKey, Task: held.Pending[0].Task,
	}}}
	m.handleEvent(events.WorkStateChanged{Snapshot: communicated})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Background result (communicated): waiting") || !strings.Contains(view, "task: weather check") || strings.Count(view, "waiting") != 1 {
		t.Fatalf("communicated result was duplicated or disappeared: %q", view)
	}
}
