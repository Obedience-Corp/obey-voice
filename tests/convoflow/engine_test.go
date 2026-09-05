package main

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestEngineFakeRunnerChatThenEmptyDrain(t *testing.T) {
	rec := &recorder{}
	rec.add(events.ResponseReady{Response: "hello"})
	fake := &fakeRunner{live: "route_only", liveOn: true, rec: rec}
	eng := &engine{
		scn: &Scenario{
			Name: "t",
			Turns: []Turn{
				{ID: "chat", User: "hi", Wait: waitIdle},
				{ID: "drain", User: "", Wait: waitIdle},
			},
		},
		runDir: t.TempDir(),
		rec:    rec,
		runner: fake,
	}
	if err := eng.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.hist) != 1 {
		t.Fatalf("history turns = %d, want 1 (empty user must not RunTurn)", len(fake.hist))
	}
}

func TestEngineEnforcesRunLevelMetrics(t *testing.T) {
	rec := &recorder{}
	fake := &fakeRunner{rec: rec, live: "route_only", liveOn: true}
	eng := &engine{
		scn: &Scenario{
			Name: "fail-metric",
			Turns: []Turn{
				{ID: "chat", User: "hi", Wait: waitIdle},
			},
			Expect: []Expectation{{Metric: "job_started", Op: ">=", Value: 1}},
		},
		runDir: t.TempDir(),
		rec:    rec,
		runner: fake,
	}
	if err := eng.run(context.Background()); err == nil {
		t.Fatal("want run-level metric failure")
	}
}
