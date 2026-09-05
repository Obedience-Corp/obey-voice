package main

import (
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func osWrite(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

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
