package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"gopkg.in/yaml.v3"
)

func interruptionPairFixture(t *testing.T, workerDuration time.Duration) (string, string) {
	t.Helper()
	o := populationFixture(t)
	root := filepath.Dir(o.Config)
	wav := filepath.Join(root, "fixture.wav")
	if err := writeControlJSON(filepath.Join(root, "fixture.json"), map[string]string{"text": "Count from one to one hundred."}); err != nil {
		t.Fatal(err)
	}
	o.RuntimeManifest = filepath.Join(root, "runtime.json")
	if err := writeControlJSON(o.RuntimeManifest, map[string]string{"test-runtime-asset": o.Binary}); err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{"baseline", "worker"} {
		prefix := "name: interruption-" + condition + "\ninput: speech\ncapture: {microphone: true, audio: true, speakers: true}\n"
		control := " - id: interruption\n   op: acoustic_interrupt\n   wav: " + wav + "\n   interrupt_wav: " + wav + "\n   expected_transcript: What is two plus two?\n"
		if condition == "worker" {
			prefix += "latency_condition: worker\nworker: {sustained_load: true}\nturns:\n - id: start\n   op: start_latency_worker\n   user: report a short local result\n" + control + " - id: stop\n   op: stop_latency_worker\n"
		} else {
			prefix += "turns:\n" + control
		}
		o.Scenario = filepath.Join(root, condition+".yaml")
		o.Output = filepath.Join(root, condition)
		if err := os.WriteFile(o.Scenario, []byte(prefix), 0600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := runControlPopulationWith(context.Background(), o, func(_ context.Context, _ string, args []string, _ string) error {
			calls++
			run := filepath.Join(args[len(args)-1], "run")
			dir := filepath.Join(run, "acoustic", "interruption")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			turns := 1
			if condition == "worker" {
				turns = 3
			}
			if err := writeControlJSON(filepath.Join(run, "metrics.json"), metricsFile{Scenario: "interruption-" + condition, StopReason: "completed", Turns: turns}); err != nil {
				return err
			}
			profile, err := yaml.Marshal(runProfile{LiveProvider: "ollama", LiveModel: "same", WorkerProvider: "ollama", WorkerModel: "model", WorkerHost: "http://127.0.0.1:11435", TTSProvider: "kokoro", WorkDir: run})
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(run, "config.resolved.yaml"), profile, 0600); err != nil {
				return err
			}
			log, err := openEventLog(filepath.Join(run, "events.jsonl"))
			if err != nil {
				return err
			}
			log.add(events.TurnMetrics{Outcome: "interrupted", Interrupted: true, AudibleSink: "local_speaker", TTSProvider: "kokoro", PlaybackStartElapsed: time.Second})
			log.add(events.TurnMetrics{Outcome: "completed", AudibleSink: "local_speaker", TTSProvider: "kokoro", PlaybackStartElapsed: time.Second})
			if err := log.Close(); err != nil {
				return err
			}
			device := audio.NegotiatedDevice{Name: "same", IdentityKnown: true, SampleRate: 48000, Channels: 1, ClientSampleRate: 16000, ClientChannels: 1}
			start := time.Now()
			load := interruptionWorkloadReport{Condition: "baseline", Scope: validInterruptionWorkload().Scope, StartedAt: start, StimulusOffset: time.Second, StoppedOffset: 2 * time.Second}
			duration := time.Millisecond
			if condition == "worker" {
				load = validInterruptionWorkload()
				duration = workerDuration
			}
			if calls <= 3 {
				duration = time.Second
			} // excluded warmups cannot fail the measured comparison
			r := interruptionReport{Workload: load, InitialTranscript: "Count from one to one hundred.", Expected: "What is two plus two?", Transcript: "What is two plus two?", ProductionInterrupted: true, StimulusCompleted: true, ResponseStarted: start, StimulusStarted: start.Add(time.Second), StimulusEnded: start.Add(2 * time.Second), InterruptedAfter: time.Second, PlaybackStoppedAfter: time.Second, DetectionToStop: duration, NegotiatedCapture: device, NegotiatedResponse: device, NegotiatedStimulus: device}
			return writeControlJSON(filepath.Join(dir, "interruption.json"), r)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "baseline"), filepath.Join(root, "worker")
}

func TestControlComparisonUsesRawMeasuredBound(t *testing.T) {
	a, b := interruptionPairFixture(t, 3*time.Millisecond)
	r, err := compareControlPopulations(a, b)
	if err != nil || r.Status != "passed" || r.Baseline.Samples != 30 || r.P95Delta != 2*time.Millisecond || r.WorstObservedBound != 2*time.Millisecond {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	a, b = interruptionPairFixture(t, 52*time.Millisecond)
	r, err = compareControlPopulations(a, b)
	if err == nil || r.Status != "failed" || r.WorstObservedBound != 51*time.Millisecond {
		t.Fatalf("regression passed: %+v %v", r, err)
	}
}

func TestControlComparisonRejectsIncompleteTamperedAndMismatchedEvidence(t *testing.T) {
	for _, kind := range []string{"incomplete", "invented timing", "missing workload", "changed artifact", "changed runtime", "different startup timing"} {
		t.Run(kind, func(t *testing.T) {
			a, b := interruptionPairFixture(t, time.Millisecond)
			s := readPopulationSummary(t, b)
			var p controlPopulationPlan
			if err := readControlJSON(filepath.Join(b, "plan.json"), &p, map[string]string{}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "incomplete":
				s.Status = "failed"
				s.Measured = 29
			case "invented timing":
				s.Samples[3].Timings["detection_to_stopped"] = time.Nanosecond
			case "missing workload":
				s.Samples[3].Workload = nil
			case "changed artifact":
				if err := os.WriteFile(filepath.Join(s.Samples[3].Run, "events.jsonl"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed runtime":
				p.Runtime["test-runtime-asset"] = "different"
			case "different startup timing":
				p.ControlSpec = "different"
			}
			if err := writeControlJSON(filepath.Join(b, "summary.json"), s); err != nil {
				t.Fatal(err)
			}
			if err := writeControlJSON(filepath.Join(b, "plan.json"), p); err != nil {
				t.Fatal(err)
			}
			if _, err := compareControlPopulations(a, b); err == nil {
				t.Fatal("invalid evidence passed")
			}
		})
	}
}

func TestPopulationControlRejectsUnboundedWorkerScenario(t *testing.T) {
	a, _ := interruptionPairFixture(t, time.Millisecond)
	scn, err := loadScenario(filepath.Join(filepath.Dir(a), "worker.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scn.Turns = append(scn.Turns, scn.Turns[1])
	if _, _, err := populationControl(scn); err == nil || !strings.Contains(err.Error(), "lifecycle") {
		t.Fatal(err)
	}
}

func TestControlScenarioIdentityIncludesCaptureHistoryAndOverrides(t *testing.T) {
	scn := &Scenario{Input: "speech"}
	tt := Turn{Op: "acoustic_interrupt", ExpectedTranscript: "Wait"}
	baseline := controlScenarioHash(scn, tt)
	scn.Capture.Microphone = true
	if controlScenarioHash(scn, tt) == baseline {
		t.Fatal("capture setting omitted")
	}
	scn.Capture.Microphone = false
	scn.Brain.Model = "different"
	if controlScenarioHash(scn, tt) == baseline {
		t.Fatal("brain override omitted")
	}
	scn.Brain.Model = ""
	scn.Persona.Initial = "different"
	if controlScenarioHash(scn, tt) == baseline {
		t.Fatal("persona omitted")
	}
}
