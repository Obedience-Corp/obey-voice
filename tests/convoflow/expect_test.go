package main

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestEvalSpokenAndHistory(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	st := turnState{
		spoken:  "Working on it.",
		history: []brain.Turn{{Role: "user", Content: "how's it going"}},
	}
	if err := evalExpect(Expectation{SpokenMatches: "(?i)working on it"}, rec, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{SpokenNotMatches: "(?i)web search is unavailable"}, rec, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{LiveHistoryExcludes: "search the web"}, rec, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{LiveHistoryIncludes: "how's it going"}, rec, st); err != nil {
		t.Fatal(err)
	}
}

func TestEvalSkipBrainLivePolicyIsVacuous(t *testing.T) {
	t.Parallel()
	st := turnState{liveBrainCalled: false, liveRouterPolicy: ""}
	if err := evalExpect(Expectation{LiveRouterPolicy: "route_only"}, &recorder{}, st); err != nil {
		t.Fatalf("skip-brain must not fail live_router_policy: %v", err)
	}
}

func TestEvalWorkerToolsRequired(t *testing.T) {
	t.Parallel()
	on := true
	st := turnState{workerCalled: true, workerToolsEnabled: &on, workerRouterPolicy: "legacy"}
	if err := evalExpect(Expectation{WorkerToolsEnabled: &on}, &recorder{}, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{WorkerRouterPolicy: "legacy"}, &recorder{}, st); err != nil {
		t.Fatal(err)
	}
	st.workerCalled = false
	if err := evalExpect(Expectation{WorkerRouterPolicy: "legacy"}, &recorder{}, st); err == nil {
		t.Fatal("want error when worker was not called")
	}
}

func TestWaitForUsesExistingLog(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.add(events.JobFinished{ID: "job-1", Summary: "done"})
	rec.add(events.SpeechSegmentReady{Text: "summary"})
	if !rec.waitSatisfied(waitJobTerminal, func() bool { return false }) {
		t.Fatal("already-finished job must satisfy job_terminal immediately")
	}
}

func TestRouteKindFromRecorder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.route(pipeline.RouteDecision{Kind: pipeline.RouteSpawn, Source: "intent"})
	d, ok := rec.lastRoute()
	if !ok {
		t.Fatal("recorder lost the decision")
	}
	st := turnState{route: &d}
	if err := evalExpect(Expectation{RouteKind: "spawn"}, rec, st); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{RouteSource: "intent"}, rec, st); err != nil {
		t.Fatal(err)
	}
}

func TestSkipRefusesStubOnGate(t *testing.T) {
	t.Parallel()
	got := skipReason(knobs{gate: true, workerProvider: "stub", liveModel: "qwen3:8b"})
	if got == "" {
		t.Fatal("gate must refuse stub")
	}
}

func TestEvalRunMetricFail(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	m := collectMetrics(rec)
	err := evalRunExpect(Expectation{Metric: "job_started", Op: ">=", Value: 1}, rec, m)
	if err == nil {
		t.Fatal("want metric failure when no JobStarted events")
	}
}

func TestEvalRunRejectsUnmeasuredCrackle(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	err := evalRunExpect(Expectation{Metric: "crackle_failing_wavs", Op: "==", Value: 0}, rec, collectMetrics(rec))
	if err == nil {
		t.Fatal("unmeasured crackle_failing_wavs must not pass")
	}
}

func TestEvalRunAcceptsMeasuredCrackleZero(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	m := collectMetrics(rec)
	m.crackleKnown = true
	m.CrackleFailingWavs = 0
	if err := evalRunExpect(Expectation{Metric: "crackle_failing_wavs", Op: "==", Value: 0}, rec, m); err != nil {
		t.Fatal(err)
	}
}

func TestEvalRunMetricPass(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.add(events.JobStarted{ID: "job-1"})
	m := collectMetrics(rec)
	if err := evalRunExpect(Expectation{Metric: "job_started", Op: ">=", Value: 1}, rec, m); err != nil {
		t.Fatal(err)
	}
}

// The follow-up turn after a spawn asserts no_event: job_started. Its slice
// starts after the spawn, so the earlier JobStarted must not leak in; and
// the run-level view (nil slice) still sees it.
func TestNamedEventsAreScopedToTheTurnSlice(t *testing.T) {
	rec := &recorder{}
	rec.add(events.JobStarted{ID: "job-1", Task: "search"})
	rec.add(events.JobFinished{ID: "job-1", Task: "search"})
	mark := len(rec.snapshot())
	rec.add(events.ResponseReady{Response: "we were talking about rust"})
	turn := turnState{events: rec.snapshot()[mark:]}

	if err := evalExpect(Expectation{NoEvent: "job_started"}, rec, turn); err != nil {
		t.Fatalf("no_event must not see the previous turn's JobStarted: %v", err)
	}
	if err := evalExpect(Expectation{Event: "job_started"}, rec, turn); err == nil {
		t.Fatal("event: job_started must not be satisfied by the previous turn")
	}
	if err := evalExpect(Expectation{Event: "job_terminal"}, rec, turn); err == nil {
		t.Fatal("event: job_terminal must not be satisfied by the previous turn")
	}
	if err := evalExpect(Expectation{Event: "response_ready"}, rec, turn); err != nil {
		t.Fatalf("this turn's own event: %v", err)
	}
	if err := evalRunExpect(Expectation{Event: "job_started"}, rec, collectMetrics(rec)); err != nil {
		t.Fatalf("run-level expect reads the whole run: %v", err)
	}
	if err := evalRunExpect(Expectation{Event: "job_terminal"}, rec, collectMetrics(rec)); err != nil {
		t.Fatalf("run-level job_terminal: %v", err)
	}
}

func TestSpokenTranscriptDedupesTheSameSentenceFromBothEvents(t *testing.T) {
	rec := &recorder{}
	rec.add(events.SpeechSegmentReady{Text: "Working on it."})
	rec.add(events.GeneratingVoice{Sentence: "Working on it."})
	rec.add(events.SpeechSegmentReady{Text: "done"})
	rec.add(events.GeneratingVoice{Sentence: "done"})
	rec.add(events.SpeechSegmentReady{Text: "done"})
	if got := rec.spokenText(); got != "Working on it. done" {
		t.Fatalf("spokenText = %q", got)
	}
}

// A route decision belongs to the turn that made it. The follow-up after a
// spawn has no route of its own, so route_kind must fail there and the
// report row must not borrow the spawn's decision.
func TestRouteExpectsAreScopedToTheTurn(t *testing.T) {
	rec := &recorder{}
	rec.route(pipeline.RouteDecision{Kind: pipeline.RouteSpawn, Source: "intent"})
	mark := rec.routeMark()
	if _, ok := rec.lastRouteSince(mark); ok {
		t.Fatal("no route after the mark must report none")
	}
	if err := evalExpect(Expectation{RouteKind: "spawn"}, rec, turnState{}); err == nil {
		t.Fatal("route_kind without a route this turn must fail")
	}
	d := pipeline.RouteDecision{Kind: pipeline.RouteJobStatus, Source: "phrase"}
	rec.route(d)
	got, ok := rec.lastRouteSince(mark)
	if !ok || got != d {
		t.Fatalf("lastRouteSince = %+v, %v", got, ok)
	}
	if err := evalExpect(Expectation{RouteKind: "job.status"}, rec, turnState{route: &got}); err != nil {
		t.Fatal(err)
	}
	if err := evalRunExpect(Expectation{RouteSource: "phrase"}, rec, collectMetrics(rec)); err != nil {
		t.Fatalf("run-level route reads the newest decision: %v", err)
	}
}

// A spawned worker's tool calls carry a job id and are the worker's, never
// the talking brain's: they must not trip live_tool_calls.
func TestWorkerToolCallsAreNotLive(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.add(events.ToolCallStarted{ID: "route#1", Name: "route", Kind: "route"})
	rec.add(events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"})
	rec.add(events.ToolCallStarted{ID: "c2", JobID: "job-1", Name: "run_command", Kind: "execute"})
	rec.add(events.ToolCallStarted{ID: "c3", Name: "list_files", Kind: "list_dir"})
	m := collectMetrics(rec)
	if m.LiveToolCalls != 1 || m.WorkerToolCalls != 2 || m.LiveRouterHit != 1 {
		t.Fatalf("live=%d worker=%d router=%d, want 1/2/1", m.LiveToolCalls, m.WorkerToolCalls, m.LiveRouterHit)
	}
	if err := evalRunExpect(Expectation{Metric: "worker_tool_calls", Op: ">=", Value: 2}, rec, m); err != nil {
		t.Fatal(err)
	}
	if err := evalRunExpect(Expectation{Metric: "live_tool_calls", Op: "==", Value: 1}, rec, m); err != nil {
		t.Fatal(err)
	}
}

// job_queued is the table accepting a job; an abort is as terminal as a
// finish for wait: job_terminal and event: job_terminal.
func TestQueuedAndAbortedJobsAreNamed(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.add(events.JobQueued{ID: "job-1", Task: "audit"})
	turn := turnState{events: rec.snapshot()}
	if err := evalExpect(Expectation{Event: "job_queued"}, rec, turn); err != nil {
		t.Fatalf("job_queued: %v", err)
	}
	if rec.waitSatisfied(waitJobTerminal, func() bool { return false }) {
		t.Fatal("a queued job must not satisfy job_terminal")
	}

	rec.add(events.JobAborted{ID: "job-1", Task: "audit"})
	if !rec.waitSatisfied(waitJobTerminal, func() bool { return false }) {
		t.Fatal("an aborted job must satisfy job_terminal")
	}
	m := collectMetrics(rec)
	if err := evalRunExpect(Expectation{Event: "job_terminal"}, rec, m); err != nil {
		t.Fatalf("event job_terminal after abort: %v", err)
	}
	if err := evalRunExpect(Expectation{Metric: "job_aborted", Op: "==", Value: 1}, rec, m); err != nil {
		t.Fatal(err)
	}
}
