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
	if err := evalExpect(Expectation{RouteKind: "spawn"}, rec, turnState{}); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{RouteSource: "intent"}, rec, turnState{}); err != nil {
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

func TestEvalRunMetricPass(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	rec.add(events.JobStarted{ID: "job-1"})
	m := collectMetrics(rec)
	if err := evalRunExpect(Expectation{Metric: "job_started", Op: ">=", Value: 1}, rec, m); err != nil {
		t.Fatal(err)
	}
}
