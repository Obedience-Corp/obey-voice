package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func latencySamples(n int) []workLatencySample {
	var out []workLatencySample
	for _, c := range []string{"baseline", "worker"} {
		for i := 0; i < n; i++ {
			s := workLatencySample{TTSProvider: "kokoro", Condition: c, Fixture: "fixture", Iteration: i + 1, Provenance: "pinned", Input: "speech", Sink: "local_speaker", Elapsed: time.Second, FirstModel: 60 * time.Millisecond, STTFinal: 50 * time.Millisecond, FirstAudio: 75 * time.Millisecond, PlaybackStart: time.Duration(100+i) * time.Millisecond}
			if c == "worker" {
				s.WorkersAtStart = 1
			}
			out = append(out, s)
		}
	}
	return out
}
func TestValidateWorkLatencyComparableThirtyEach(t *testing.T) {
	r, e := validateWorkLatency(latencySamples(30))
	if e != nil || r.Conditions["baseline"].Samples != 30 {
		t.Fatalf("report=%+v err=%v", r, e)
	}
	if r.Conditions["baseline"].P95Response != 78*time.Millisecond {
		t.Fatalf("p95=%s", r.Conditions["baseline"].P95Response)
	}
}
func TestValidateWorkLatencyRejectsInvalidEvidence(t *testing.T) {
	cases := map[string]func([]workLatencySample) []workLatencySample{
		"too few": func(s []workLatencySample) []workLatencySample { return append(s[:29], s[30:59]...) },
		"entire other provenance": func(s []workLatencySample) []workLatencySample {
			for i := 30; i < len(s); i++ {
				s[i].Provenance = "other"
			}
			return s
		},
		"duplicate":                  func(s []workLatencySample) []workLatencySample { s[1] = s[0]; return s },
		"failed warmup":              func(s []workLatencySample) []workLatencySample { s[0].Warmup = true; s[0].Error = "timeout"; return s },
		"text":                       func(s []workLatencySample) []workLatencySample { s[0].Input = "text"; return s },
		"recording only":             func(s []workLatencySample) []workLatencySample { s[0].Sink = "none"; return s },
		"no actual worker":           func(s []workLatencySample) []workLatencySample { s[30].WorkersAtStart = 0; return s },
		"baseline work":              func(s []workLatencySample) []workLatencySample { s[0].WorkersAtStart = 1; return s },
		"pending update":             func(s []workLatencySample) []workLatencySample { s[0].PendingAtStart = 1; return s },
		"missing timing":             func(s []workLatencySample) []workLatencySample { s[0].FirstAudio = 0; return s },
		"clock order":                func(s []workLatencySample) []workLatencySample { s[0].PlaybackStart = s[0].STTFinal; return s },
		"different fixture":          func(s []workLatencySample) []workLatencySample { s[0].Fixture = "other"; return s },
		"warmup omitted on one side": func(s []workLatencySample) []workLatencySample { s[0].Warmup = true; return s },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validateWorkLatency(mut(latencySamples(30))); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
func TestCompareUsesGeneratedMetricsSchemaAndRejectsFailedRun(t *testing.T) {
	dir := t.TempDir()
	samples := latencySamples(30)
	write := func(name, condition, status string, s []workLatencySample) string {
		t.Helper()
		e := &engine{scn: &Scenario{Name: name, LatencyCondition: condition}, runDir: filepath.Join(dir, name), rec: &recorder{}, latencySamples: s, stopReason: status}
		if err := os.MkdirAll(e.runDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeMetricsJSON(e, runMetrics{}); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(e.runDir, "metrics.json")
	}
	a := write("a", "baseline", "completed", samples[:30])
	b := write("b", "worker", "completed", samples[30:])
	if _, err := compareWorkLatencyFiles(a, b); err != nil {
		t.Fatal(err)
	}
	b = write("b", "worker", "turn_failed", samples[30:])
	if _, err := compareWorkLatencyFiles(a, b); err == nil {
		t.Fatal("failed run accepted")
	}
	if err := os.WriteFile(b, []byte(`{"latency":{"schema":"unknown"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := compareWorkLatencyFiles(a, b); err == nil {
		t.Fatal("unknown schema accepted")
	}
}

type latencyReplayRunner struct {
	fakeRunner
	called bool
	fail   bool
}

func (r *latencyReplayRunner) runWAV(_ context.Context, _ string) error {
	r.called = true
	r.rec.add(events.TurnMetrics{TTSProvider: "kokoro", Outcome: "completed", STTFinalElapsed: 50 * time.Millisecond, FirstModelChunkElapsed: 60 * time.Millisecond, FirstAudioReadyElapsed: 75 * time.Millisecond, PlaybackStartElapsed: 100 * time.Millisecond, AudibleSink: "none"})
	if r.fail {
		return errors.New("controlled replay failure")
	}
	return nil
}
func TestEngineRoutesWAVAndRetainsFailedRawSample(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			d := t.TempDir()
			fixture := filepath.Join(d, "input.wav")
			if err := os.WriteFile(fixture, []byte("controlled fixture bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			rec := &recorder{}
			r := &latencyReplayRunner{fakeRunner: fakeRunner{rec: rec}, fail: fail}
			e := &engine{scn: &Scenario{Name: "timing", Input: "speech", LatencyCondition: "baseline", Turns: []Turn{{ID: "one", WAV: fixture, LatencyIteration: 1}}}, runDir: d, rec: rec, runner: r, latencyProvenance: "fixture-config"}
			err := e.run(context.Background())
			if fail != (err != nil) {
				t.Fatalf("run=%v", err)
			}
			if !r.called {
				t.Fatal("WAV routed through text")
			}
			raw, err := os.ReadFile(filepath.Join(d, "metrics.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m metricsFile
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if m.Latency == nil || len(m.Latency.Samples) != 1 {
				t.Fatalf("missing sample: %s", raw)
			}
			s := m.Latency.Samples[0]
			if s.STTFinal != 50*time.Millisecond || s.PlaybackStart != 100*time.Millisecond || s.Fixture == "" || s.Input != "speech" {
				t.Fatalf("raw sample=%+v", s)
			}
			if fail && !strings.Contains(s.Error, "controlled replay failure") {
				t.Fatalf("lost failure: %+v", s)
			}
			if _, err := validateWorkLatency(m.Latency.Samples); err == nil {
				t.Fatal("recording-only fixture accepted as live acceptance")
			}
		})
	}
}

type failingAudioReplay struct {
	fakeRunner
	player *recordingPlayer
}

func (r *failingAudioReplay) runWAV(ctx context.Context, _ string) error {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		return err
	}
	playback, err := r.player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}
	if err := stream.Write(synthSine(24000, 480, 440, .1)); err != nil {
		return err
	}
	stream.Close()
	select {
	case result := <-playback.Done():
		if result.Err != nil {
			return result.Err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return errors.New("failure after partial PCM")
}
func TestFailedReplayFinalizesPartialAudioAndClosesRecorder(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	player := newRecordingPlayer(nil)
	r := &failingAudioReplay{fakeRunner: fakeRunner{rec: rec}, player: player}
	e := &engine{scn: &Scenario{Name: "partial", Input: "speech", Turns: []Turn{{ID: "partial", WAV: "controlled.wav"}}}, runDir: dir, rec: rec, player: player, runner: r}
	if err := e.run(context.Background()); err == nil || !strings.Contains(err.Error(), "failure after partial PCM") {
		t.Fatalf("run=%v", err)
	}
	for _, name := range []string{"playback.wav", "analysis.json"} {
		if stat, err := os.Stat(filepath.Join(dir, "audio", "01-partial", name)); err != nil || stat.Size() == 0 {
			t.Fatalf("partial artifact %s: %v", name, err)
		}
	}
	player.mu.Lock()
	closed := player.closed
	player.mu.Unlock()
	if !closed {
		t.Fatal("recording player not closed")
	}
}
