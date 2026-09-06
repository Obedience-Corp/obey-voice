package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestEngineFakeRunnerChatThenEmptyDrain(t *testing.T) {
	rec := &recorder{}
	rec.add(events.ResponseReady{Response: "hello"})
	fake := &fakeRunner{live: "route_only", liveOn: true, rec: rec}
	runDir := t.TempDir()
	eng := &engine{
		scn: &Scenario{
			Name: "t",
			Turns: []Turn{
				{ID: "chat", User: "hi", Wait: waitIdle},
				{ID: "drain", User: "", Wait: waitIdle},
			},
		},
		runDir: runDir,
		rec:    rec,
		runner: fake,
		player: newRecordingPlayer(nil),
	}
	if err := eng.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.hist) != 1 {
		t.Fatalf("history turns = %d, want 1 (empty user must not RunTurn)", len(fake.hist))
	}
	if _, err := os.Stat(filepath.Join(runDir, "audio", "01-chat", "analysis.json")); err != nil {
		t.Fatalf("analysis.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "metrics.json")); err != nil {
		t.Fatalf("metrics.json: %v", err)
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

func TestEngineScoresCleanPlayback(t *testing.T) {
	rec := &recorder{}
	player := newRecordingPlayer(nil)
	pcm := synthSine(24_000, 2_400, 440, 0.4)
	fake := &pcmRunner{
		fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true},
		player:     player,
		pcm:        pcm,
		rate:       24_000,
	}
	eng := &engine{
		scn: &Scenario{
			Name: "audio-ok",
			Turns: []Turn{
				{ID: "chat", User: "hi", Wait: waitIdle},
			},
			Expect: []Expectation{{Metric: "crackle_failing_wavs", Op: "==", Value: 0}},
		},
		runDir: t.TempDir(),
		rec:    rec,
		runner: fake,
		player: player,
	}
	if err := eng.run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngineFailsCrackleMetric(t *testing.T) {
	rec := &recorder{}
	player := newRecordingPlayer(nil)
	pcm := synthSine(24_000, 8_000, 440, 0.4)
	for i := 0; i < 12; i++ {
		off := 400 + i*500
		pcm[off] = 1
		pcm[off+1] = -1
	}
	fake := &pcmRunner{
		fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true},
		player:     player,
		pcm:        pcm,
		rate:       24_000,
	}
	eng := &engine{
		scn: &Scenario{
			Name: "audio-bad",
			Turns: []Turn{
				{ID: "chat", User: "hi", Wait: waitIdle},
			},
			Expect: []Expectation{{Metric: "crackle_failing_wavs", Op: "==", Value: 0}},
		},
		runDir: t.TempDir(),
		rec:    rec,
		runner: fake,
		player: player,
	}
	if err := eng.run(context.Background()); err == nil {
		t.Fatal("want crackle_failing_wavs failure")
	}
}

type pcmRunner struct {
	fakeRunner
	player *recordingPlayer
	pcm    []float32
	rate   int
}

func (r *pcmRunner) runTurn(ctx context.Context, user string) error {
	if err := r.fakeRunner.runTurn(ctx, user); err != nil {
		return err
	}
	if r.player == nil || len(r.pcm) == 0 {
		return nil
	}
	return playPCM(ctx, r.player, r.rate, r.pcm)
}
