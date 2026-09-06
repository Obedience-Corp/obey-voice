package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A scenario's duration is the whole-run wall: a runner that never returns
// is cut off, the report is still written, and stop_reason says why.
func TestEngineScenarioDurationIsTheWall(t *testing.T) {
	rec := &recorder{}
	runDir := t.TempDir()
	eng := &engine{
		scn: &Scenario{
			Name:     "wall",
			Duration: duration{50 * time.Millisecond},
			Turns:    []Turn{{ID: "hang", User: "hi", Wait: waitIdle, Timeout: duration{time.Minute}}},
		},
		runDir: runDir,
		rec:    rec,
		runner: &blockingRunner{fakeRunner: fakeRunner{rec: rec}},
	}
	start := time.Now()
	err := eng.run(context.Background())
	if err == nil {
		t.Fatal("want the wall to fail the run")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("run took %s; the wall did not cut it off", time.Since(start))
	}
	if eng.stopReason != "timeout" {
		t.Fatalf("stop_reason = %q, want timeout", eng.stopReason)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metrics.json"))
	if err != nil {
		t.Fatalf("metrics.json must be written on failure: %v", err)
	}
	if !strings.Contains(string(data), `"stop_reason": "timeout"`) {
		t.Fatalf("metrics.json = %s", data)
	}
}

func TestEngineReportsTurnsAndFailedRunExpects(t *testing.T) {
	rec := &recorder{}
	fake := &fakeRunner{rec: rec, live: "route_only", liveOn: true}
	runDir := t.TempDir()
	eng := &engine{
		scn: &Scenario{
			Name:   "rows",
			Turns:  []Turn{{ID: "chat", User: "hi", Wait: waitIdle}},
			Expect: []Expectation{{Metric: "job_started", Op: ">=", Value: 1}},
		},
		runDir:  runDir,
		rec:     rec,
		runner:  fake,
		profile: runProfile{Profile: "stub", LiveProvider: "ollama", LiveModel: "qwen3:8b", WorkerProvider: "stub"},
	}
	if err := eng.run(context.Background()); err == nil {
		t.Fatal("want run-level metric failure")
	}
	if eng.stopReason != "run_expect_failed" {
		t.Fatalf("stop_reason = %q", eng.stopReason)
	}
	report, err := os.ReadFile(filepath.Join(runDir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| chat | hi |", "live: ollama qwen3:8b", "stop_reason: run_expect_failed"} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("report.md missing %q:\n%s", want, report)
		}
	}
}

type blockingRunner struct{ fakeRunner }

func (b *blockingRunner) runTurn(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}
