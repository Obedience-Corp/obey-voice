package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"gopkg.in/yaml.v3"
)

func populationFixture(t *testing.T) controlPopulationOptions {
	t.Helper()
	root := t.TempDir()
	scenario := `name: completion
input: speech
capture: {microphone: true, audio: true, speakers: true}
turns:
  - id: completion
    op: acoustic_completion
    wav: FIXTURE
    expected_transcript: What is two plus two?
`
	wav := filepath.Join(root, "fixture.wav")
	for name, data := range map[string]string{"fixture.wav": "pinned fixture", "binary": "pinned binary", "config.yaml": "pinned config", "scenario.yaml": strings.ReplaceAll(scenario, "FIXTURE", wav)} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return controlPopulationOptions{Output: filepath.Join(root, "population"), Scenario: filepath.Join(root, "scenario.yaml"), Config: filepath.Join(root, "config.yaml"), Binary: filepath.Join(root, "binary"), Samples: 30, Warmups: 3, Timeout: time.Second}
}

func fakeControlChild(t *testing.T, modify func(*completionReport)) controlChild {
	t.Helper()
	return func(ctx context.Context, _ string, args []string, _ string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		run := filepath.Join(args[len(args)-1], "completion-sample")
		dir := filepath.Join(run, "acoustic", "completion")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		m := metricsFile{Scenario: "completion", StopReason: "completed", Turns: 1}
		if err := writeControlJSON(filepath.Join(run, "metrics.json"), m); err != nil {
			return err
		}
		profile := runProfile{LiveProvider: "ollama", LiveModel: "pinned", TTSProvider: "kokoro", WorkDir: filepath.Join(run, "work")}
		p, err := yaml.Marshal(profile)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(run, "config.resolved.yaml"), p, 0600); err != nil {
			return err
		}
		log, err := openEventLog(filepath.Join(run, "events.jsonl"))
		if err != nil {
			return err
		}
		for range 2 {
			log.add(events.TurnMetrics{TTSProvider: "kokoro", TTSPlace: "local", Outcome: "completed", AudibleSink: "local_speaker", PlaybackStartElapsed: time.Second})
		}
		if err := log.Close(); err != nil {
			return err
		}
		d := audio.NegotiatedDevice{Name: "Selected device", IdentityKnown: true, SampleRate: 48000, Channels: 1, ClientSampleRate: 16000, ClientChannels: 1}
		r := completionReport{Expected: "What is two plus two?", Transcript: "What is two plus two?", PauseMS: 1.25, CompletionMS: 1250, Acknowledged: true, Yielded: true, Communicated: true, NegotiatedCapture: d, NegotiatedResponse: d, NegotiatedStimulus: d}
		if modify != nil {
			modify(&r)
		}
		return writeControlJSON(filepath.Join(dir, "completion.json"), r)
	}
}

func readPopulationSummary(t *testing.T, dir string) controlPopulationSummary {
	t.Helper()
	var s controlPopulationSummary
	if err := readControlJSON(filepath.Join(dir, "summary.json"), &s, make(map[string]string)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestControlPopulationRetainsFailureWithoutReplacement(t *testing.T) {
	o := populationFixture(t)
	calls := 0
	child := fakeControlChild(t, func(r *completionReport) {
		calls++
		if calls == 5 {
			r.Transcript = "wrong phrase"
		}
	})
	if err := runControlPopulationWith(context.Background(), o, child); err == nil {
		t.Fatal("bad transcript passed")
	}
	s := readPopulationSummary(t, o.Output)
	if calls != 5 || s.Status != "failed" || len(s.Samples) != 5 || s.Measured != 1 || s.Timings != nil || s.Samples[4].Error == "" {
		t.Fatalf("false population success/retry: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(o.Output, "sample-005", "sample.json")); err != nil {
		t.Fatal(err)
	}
	if err := runControlPopulationWith(context.Background(), o, child); err == nil {
		t.Fatal("existing population overwritten")
	}
	if calls != 5 {
		t.Fatal("overwrite attempted another child")
	}
}

func TestControlPopulationExcludesWarmupsAndRejectsIdentityDrift(t *testing.T) {
	t.Run("warmups", func(t *testing.T) {
		o := populationFixture(t)
		calls := 0
		err := runControlPopulationWith(context.Background(), o, fakeControlChild(t, func(r *completionReport) {
			calls++
			if calls <= 3 {
				r.PauseMS = 299
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		s := readPopulationSummary(t, o.Output)
		if s.Status != "completed" || s.Measured != 30 || calls != 33 || s.Timings["pause"].P95 != 1250*time.Microsecond {
			t.Fatalf("warmups contaminated measured timings: %+v", s)
		}
	})
	t.Run("route drift", func(t *testing.T) {
		o := populationFixture(t)
		calls := 0
		err := runControlPopulationWith(context.Background(), o, fakeControlChild(t, func(r *completionReport) {
			calls++
			if calls == 2 {
				r.NegotiatedCapture.Name = "Other device"
			}
		}))
		if err == nil || !strings.Contains(err.Error(), "device changed") || calls != 2 {
			t.Fatalf("route drift: %v calls%d", err, calls)
		}
	})
}

func TestControlPopulationRejectsChangedInputAndCancellation(t *testing.T) {
	t.Run("input changed during child", func(t *testing.T) {
		o := populationFixture(t)
		real := fakeControlChild(t, nil)
		calls := 0
		err := runControlPopulationWith(context.Background(), o, func(ctx context.Context, b string, a []string, l string) error {
			calls++
			if err := real(ctx, b, a, l); err != nil {
				return err
			}
			return os.WriteFile(o.Config, []byte("changed config"), 0600)
		})
		if err == nil || !strings.Contains(err.Error(), "pinned input changed") || calls != 1 {
			t.Fatalf("input drift: %v", err)
		}
	})
	t.Run("canceled child", func(t *testing.T) {
		o := populationFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		err := runControlPopulationWith(ctx, o, func(ctx context.Context, _ string, _ []string, _ string) error { calls++; cancel(); return ctx.Err() })
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("cancellation: %v", err)
		}
		if s := readPopulationSummary(t, o.Output); s.Status != "failed" || s.Timings != nil {
			t.Fatal("canceled population passed")
		}
	})
}

func TestControlPopulationRejectsFalsePassArtifacts(t *testing.T) {
	for name, change := range map[string]func(*completionReport){
		"no acknowledgement": func(r *completionReport) { r.Acknowledged = false },
		"no yield":           func(r *completionReport) { r.Yielded = false },
		"no receipt":         func(r *completionReport) { r.Communicated = false },
		"capture unknown":    func(r *completionReport) { r.NegotiatedCapture.IdentityKnown = false },
		"response failed":    func(r *completionReport) { r.Error = "playback failed" },
		"missing duration":   func(r *completionReport) { r.CompletionMS = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			o := populationFixture(t)
			if err := runControlPopulationWith(context.Background(), o, fakeControlChild(t, change)); err == nil {
				t.Fatal("invalid physical report passed")
			}
			s := readPopulationSummary(t, o.Output)
			if len(s.Samples) != 1 || s.Measured != 0 || s.Timings != nil {
				t.Fatal("invalid report entered population")
			}
		})
	}
}

func TestControlPopulationRejectsFallbackAndCrackle(t *testing.T) {
	for _, failure := range []string{"fallback", "degraded", "changed engine", "crackle", "missing metrics"} {
		t.Run(failure, func(t *testing.T) {
			o := populationFixture(t)
			valid := fakeControlChild(t, nil)
			err := runControlPopulationWith(context.Background(), o, func(ctx context.Context, b string, a []string, l string) error {
				if err := valid(ctx, b, a, l); err != nil {
					return err
				}
				run := filepath.Join(a[len(a)-1], "completion-sample")
				if failure == "crackle" {
					one := 1
					return writeControlJSON(filepath.Join(run, "metrics.json"), metricsFile{Scenario: "completion", StopReason: "completed", Turns: 1, CrackleFailingWavs: &one})
				}
				log, err := openEventLog(filepath.Join(run, "events.jsonl"))
				if err != nil {
					return err
				}
				m := events.TurnMetrics{TTSProvider: "kokoro", TTSPlace: "local", Outcome: "completed", AudibleSink: "local_speaker", PlaybackStartElapsed: time.Second}
				switch failure {
				case "fallback":
					m.TTSFallbacks = 1
				case "degraded":
					m.Degraded = true
				case "changed engine":
					m.TTSProvider = "other"
				}
				if failure != "missing metrics" {
					for range 2 {
						log.add(m)
					}
				}
				return log.Close()
			})
			if err == nil {
				t.Fatal("invalid actual voice accepted")
			}
			if s := readPopulationSummary(t, o.Output); s.Measured != 0 || s.Timings != nil {
				t.Fatal("failed voice entered quantiles")
			}
		})
	}
}

func TestControlPopulationInterruptRequiresCorrectInitialSpeech(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			o := populationFixture(t)
			wav := filepath.Join(filepath.Dir(o.Config), "fixture.wav")
			data := []byte("name: interruption\ninput: speech\ncapture: {microphone: true, audio: true, speakers: true}\nturns:\n - id: interruption\n   op: acoustic_interrupt\n   wav: " + wav + "\n   interrupt_wav: " + wav + "\n   expected_transcript: What is two plus two?\n")
			if err := os.WriteFile(o.Scenario, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeControlJSON(strings.TrimSuffix(wav, ".wav")+".json", map[string]string{"text": "Count from one to one hundred."}); err != nil {
				t.Fatal(err)
			}
			child := func(ctx context.Context, _ string, args []string, _ string) error {
				run := filepath.Join(args[len(args)-1], "interruption-sample")
				dir := filepath.Join(run, "acoustic", "interruption")
				if err := os.MkdirAll(dir, 0700); err != nil {
					return err
				}
				if err := writeControlJSON(filepath.Join(run, "metrics.json"), metricsFile{Scenario: "interruption", StopReason: "completed", Turns: 1}); err != nil {
					return err
				}
				p, err := yaml.Marshal(runProfile{LiveProvider: "ollama", TTSProvider: "kokoro"})
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(run, "config.resolved.yaml"), p, 0600); err != nil {
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
				d := audio.NegotiatedDevice{Name: "Selected", IdentityKnown: true, SampleRate: 48000, Channels: 1, ClientSampleRate: 16000, ClientChannels: 1}
				start := time.Now()
				r := interruptionReport{InitialTranscript: "Count from one to one hundred.", Expected: "What is two plus two?", Transcript: "What is two plus two?", ProductionInterrupted: true, StimulusCompleted: true, ResponseStarted: start, StimulusStarted: start.Add(time.Second), StimulusEnded: start.Add(2 * time.Second), InterruptedAfter: time.Second, PlaybackStoppedAfter: time.Second, DetectionToStop: time.Microsecond, NegotiatedCapture: d, NegotiatedResponse: d, NegotiatedStimulus: d}
				if wrong {
					r.InitialTranscript = "What country is this?"
				}
				return writeControlJSON(filepath.Join(dir, "interruption.json"), r)
			}
			err := runControlPopulationWith(context.Background(), o, child)
			if wrong && err == nil {
				t.Fatal("changed initial question passed")
			}
			if !wrong && err != nil {
				t.Fatal(err)
			}
		})
	}
}
