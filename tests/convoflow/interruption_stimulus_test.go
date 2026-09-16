package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type interruptionDelayRunner struct {
	fakeRunner
	delays  []time.Duration
	windows []time.Duration
	paths   []string
}

func (r *interruptionDelayRunner) runAcousticInterruption(_ context.Context, _, _, _, artifactDir string, options interruptionStimulusOptions) error {
	r.delays = append(r.delays, options.delay)
	r.windows = append(r.windows, options.completeWithin)
	r.paths = append(r.paths, artifactDir)
	return nil
}

func TestEngineForwardsPerTurnInterruptionStimulusDelay(t *testing.T) {
	rec := &recorder{}
	r := &interruptionDelayRunner{fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true}}
	e := &engine{
		scn: &Scenario{Name: "delays", Turns: []Turn{
			{ID: "default", Op: "acoustic_interrupt", Wait: waitIdle},
			{ID: "early", Op: "acoustic_interrupt", Wait: waitIdle, StimulusDelay: &duration{}, StimulusCompleteWithin: &duration{600 * time.Millisecond}},
			{ID: "custom", Op: "acoustic_interrupt", Wait: waitIdle, StimulusDelay: &duration{75 * time.Millisecond}},
		}},
		runDir: t.TempDir(), rec: rec, runner: r,
	}
	if err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{900 * time.Millisecond, 0, 75 * time.Millisecond}
	windows := []time.Duration{0, 600 * time.Millisecond, 0}
	if len(r.delays) != len(want) {
		t.Fatalf("calls = %d, want %d", len(r.delays), len(want))
	}
	for i, delay := range want {
		if r.delays[i] != delay || r.windows[i] != windows[i] || r.paths[i] != filepath.Join(e.runDir, "acoustic", e.scn.Turns[i].ID) {
			t.Fatalf("turn %d: delay=%s path=%q", i, r.delays[i], r.paths[i])
		}
	}
}

func TestInterruptionStimulusReportUsesObservedInterval(t *testing.T) {
	response := time.Now()
	for _, tc := range []struct {
		name       string
		start, end time.Duration
		err        error
	}{
		{"early-short", 40 * time.Millisecond, 350 * time.Millisecond, nil},
		{"late-start-despite-zero-delay", 650 * time.Millisecond, 950 * time.Millisecond, nil},
		{"long-despite-short-fixture", 50 * time.Millisecond, 750 * time.Millisecond, nil},
		{"canceled-inside-guard", 30 * time.Millisecond, 200 * time.Millisecond, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := interruptionReport{ResponseStarted: response, RequestedStimulusDelay: 0, RequestedStimulusCompleteWithin: 600 * time.Millisecond, StimulusFixtureDuration: 300 * time.Millisecond}
			report.recordStimulus(interruptionStimulusObservation{started: response.Add(tc.start), ended: response.Add(tc.end), err: tc.err})
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var saved interruptionReport
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.StimulusStartAfterResponse != tc.start || saved.StimulusEndAfterResponse != tc.end || saved.StimulusPlaybackDuration != tc.end-tc.start {
				t.Fatalf("observed interval lost: %s", data)
			}
			if saved.StimulusCompleted != (tc.err == nil) || saved.RequestedStimulusDelay != 0 || saved.StimulusFixtureDuration != 300*time.Millisecond {
				t.Fatalf("completion/configuration lost: %s", data)
			}
			if err := saved.validateStimulusWindow(); (err == nil) != (tc.name == "early-short") {
				t.Fatalf("wrong observed startup verdict for %s: %v", tc.name, err)
			}
			if tc.err != nil && saved.StimulusError != tc.err.Error() {
				t.Fatalf("error lost: %s", data)
			}
		})
	}
}

func TestInterruptionStimulusObservationSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan time.Time, 1)
	done := make(chan interruptionStimulusObservation, 1)
	go func() {
		done <- observeInterruptionStimulus(func(onStart func(time.Time)) error {
			onStart(time.Now())
			<-ctx.Done()
			return ctx.Err()
		}, started)
	}()
	var start time.Time
	select {
	case start = <-started:
	case <-time.After(time.Second):
		t.Fatal("start observation missing")
	}
	cancel()
	select {
	case observation := <-done:
		if !observation.started.Equal(start) || observation.ended.Before(start) || !errors.Is(observation.err, context.Canceled) {
			t.Fatalf("observation = %+v", observation)
		}
		report := interruptionReport{ResponseStarted: start}
		report.recordStimulus(observation)
		if report.StimulusCompleted || report.StimulusEnded.IsZero() {
			t.Fatalf("canceled playback report = %+v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("stimulus did not join after cancellation")
	}
}
