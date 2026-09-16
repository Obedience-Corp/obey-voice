package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func boundaryHealthyCapture() acousticReport {
	return acousticReport{Transcript: "What is the capital of France?", CaptureReadSamples: 48000, CapturedSamples: 50000, StimulusWindowValid: true, StimulusAudible: true, EnvelopeCorrelation: .8, CapturedPeak: .1, NonzeroReferenceSamples: 5000}
}
func boundaryEmit(bus *events.Bus, phase string) {
	bus.Emit(events.UserInput{Text: "What is the capital of France?"})
	response := "Paris."
	degraded := phase == "failure"
	sink := "local_speaker"
	if degraded {
		response = brain.RecoveryReply
		bus.Emit(events.Error{Stage: "brain", Message: acousticProviderFailure})
	}
	if phase == "muted" {
		sink = "none"
	} else {
		bus.Emit(events.SpeakingStarted{AudibleSink: sink})
		bus.Emit(events.SpeakingComplete{AudibleSink: sink, Elapsed: time.Second})
	}
	bus.Emit(events.ResponseReady{Response: response, Degraded: degraded})
	bus.Emit(events.TurnMetrics{Outcome: "completed", AudibleSink: sink, Degraded: degraded})
}
func TestAcousticBoundaryAcceptsActualMuteAndDegradedSemantics(t *testing.T) {
	for _, phase := range []string{"muted", "failure", "recovered"} {
		t.Run(phase, func(t *testing.T) {
			bus := events.NewBus()
			a := boundaryHealthyCapture()
			if phase == "muted" {
				a.NonzeroReferenceSamples = 0
			}
			r, e := observeBoundaryPhase(t.Context(), bus, phase, a.Transcript, func(context.Context) error { boundaryEmit(bus, phase); return nil }, func() (acousticReport, error) { return a, nil })
			if e != nil || r.AudibleDeliveryCompleted != (phase != "muted") {
				t.Fatalf("report=%+v err=%v", r, e)
			}
		})
	}
}
func TestAcousticBoundaryRejectsFalsePasses(t *testing.T) {
	for _, mode := range []string{"silent_muted_reference", "muted_claimed_speaker", "duplicate_input", "duplicate_response", "missing_public_error", "wrong_public_error", "failure_claimed_success", "missing_completion", "interrupted", "unhealthy_capture", "stalled_capture", "wrong_capture_transcript", "capture_error", "early_turn", "cancelled", "joined_hardware_error"} {
		t.Run(mode, func(t *testing.T) {
			phase := "recovered"
			if strings.HasPrefix(mode, "muted") || mode == "silent_muted_reference" {
				phase = "muted"
			}
			if strings.Contains(mode, "error") || mode == "failure_claimed_success" {
				phase = "failure"
			}
			bus := events.NewBus()
			a := boundaryHealthyCapture()
			if phase == "muted" {
				a.NonzeroReferenceSamples = 0
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, e := observeBoundaryPhase(ctx, bus, phase, a.Transcript, func(context.Context) error {
				if mode == "early_turn" {
					return nil
				}
				if mode == "missing_public_error" || mode == "failure_claimed_success" {
					boundaryEmit(bus, "recovered")
				} else {
					boundaryEmit(bus, phase)
				}
				switch mode {
				case "silent_muted_reference":
					a.NonzeroReferenceSamples = 1
				case "muted_claimed_speaker":
					bus.Emit(events.SpeakingStarted{AudibleSink: "local_speaker"})
				case "duplicate_input":
					bus.Emit(events.UserInput{Text: a.Transcript})
				case "duplicate_response":
					bus.Emit(events.ResponseReady{Response: "Paris."})
				case "wrong_public_error":
					bus.Emit(events.Error{Stage: "tts", Message: "extra unrelated failure"})
				case "missing_completion":
					bus.Emit(events.SpeakingStarted{AudibleSink: "local_speaker"})
				case "interrupted":
					bus.Emit(events.SpeakingInterrupted{Reason: "barge_in"})
				case "unhealthy_capture":
					a.StimulusAudible = false
				case "stalled_capture":
					a.CaptureReadSamples = 0
				case "wrong_capture_transcript":
					a.Transcript = "Lost question"
				case "capture_error":
					a.Error = "microphone disconnected"
				case "cancelled":
					cancel()
				case "joined_hardware_error":
					return errors.Join(context.Canceled, errors.New("device error"))
				}
				return nil
			}, func() (acousticReport, error) { return a, nil })
			if e == nil {
				t.Fatalf("false pass: %+v", r)
			}
		})
	}
}
func TestAcousticBoundaryObservesProductionProviderFailure(t *testing.T) {
	bus := events.NewBus()
	original := &replayBrain{}
	failed := &boundaryFailureProvider{Provider: original}
	p := &pipeline.Pipeline{STT: &replaySTT{text: "What is the capital of France?"}, Brain: failed, Events: bus}
	var errorsSeen []events.Error
	var responses []events.ResponseReady
	var metrics []events.TurnMetrics
	remove := bus.ObserveBeforeFanout(func(e events.Event) {
		switch e := e.(type) {
		case events.Error:
			errorsSeen = append(errorsSeen, e)
		case events.ResponseReady:
			responses = append(responses, e)
		case events.TurnMetrics:
			metrics = append(metrics, e)
		}
	})
	defer remove()
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if failed.calls.Load() != 1 || len(errorsSeen) != 1 || errorsSeen[0].Stage != "brain" || len(responses) != 1 || responses[0].Response != brain.RecoveryReply || !responses[0].Degraded || len(metrics) != 1 || metrics[0].Outcome != "completed" || !metrics[0].Degraded {
		t.Fatalf("production failure facts: errors=%+v responses=%+v metrics=%+v calls=%d", errorsSeen, responses, metrics, failed.calls.Load())
	}
	p.Brain = original
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(responses) != 2 || responses[1].Degraded || len(metrics) != 2 || metrics[1].Degraded || failed.calls.Load() != 1 {
		t.Fatal("provider not restored for fresh next turn")
	}
}
func TestAcousticBoundaryMutedProductionTurnKeepsTextWithoutSpeech(t *testing.T) {
	bus := events.NewBus()
	p := &pipeline.Pipeline{STT: &replaySTT{text: "What is the capital of France?"}, Brain: &replayBrain{}, Events: bus}
	p.SetOutputMuted(true)
	defer p.SetOutputMuted(false)
	a := boundaryHealthyCapture()
	a.NonzeroReferenceSamples = 0
	_, err := observeBoundaryPhase(t.Context(), bus, "muted", a.Transcript, func(ctx context.Context) error { _, err := p.RunTurn(ctx); return err }, func() (acousticReport, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
}
func TestAcousticBoundaryCanceledReadinessJoinsSTT(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	bus := events.NewBus()
	joined := make(chan struct{})
	_, err := runAcousticTurn(ctx, bus, func(ctx context.Context) (string, error) { defer close(joined); <-ctx.Done(); return "", ctx.Err() }, func(context.Context) error { t.Fatal("canceled control played stimulus"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("turn not joined")
	}
}
func TestAcousticBoundaryRejectsUnrelatedReceipts(t *testing.T) {
	if err := boundaryNoWork(work.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []work.Snapshot{{Jobs: []work.JobView{{}}}, {Pending: []work.ResultView{{}}}, {Communicated: []work.ResultView{{}}}} {
		if boundaryNoWork(s) == nil {
			t.Fatal("unrelated work accepted")
		}
	}
}

type boundaryRunner struct {
	fakeRunner
	op, wav, expected, dir string
}

func (r *boundaryRunner) runAcousticBoundary(_ context.Context, op, wav, expected, dir string) error {
	r.op, r.wav, r.expected, r.dir = op, wav, expected, dir
	return nil
}
func TestAcousticBoundaryScenariosAndEngine(t *testing.T) {
	for _, op := range []string{"acoustic_mute", "acoustic_failure"} {
		body := "name: boundary\ninput: speech\ncapture: {microphone: true, speakers: true}\nturns:\n - id: boundary\n   op: " + op + "\n   wav: phrase.wav\n   expected_transcript: What is the capital of France?\n"
		scn, err := loadScenarioString("boundary", body)
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		r := &boundaryRunner{fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true}}
		e := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: r}
		if err := e.run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if r.op != op || r.wav != "phrase.wav" || r.dir != filepath.Join(e.runDir, "acoustic", "boundary") {
			t.Fatalf("wrong forwarding %+v", r)
		}
		for _, bad := range []string{strings.Replace(body, "microphone: true", "microphone: false", 1), strings.Replace(body, "   wav: phrase.wav\n", "", 1), strings.Replace(body, "   expected_transcript: What is the capital of France?\n", "", 1), body + "   duration: 1s\n", body + "   interrupt_wav: extra.wav\n", "tts: {provider: none}\n" + body, "latency_condition: baseline\n" + body} {
			if _, err := loadScenarioString("bad", bad); err == nil {
				t.Fatalf("accepted invalid %s", bad)
			}
		}
	}
}

func TestAcousticBoundaryStalledTurnHonorsDeadlineAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	bus := events.NewBus()
	joined := make(chan struct{})
	played := false
	_, err := runAcousticTurn(ctx, bus, func(ctx context.Context) (string, error) {
		defer close(joined)
		bus.Emit(events.STTPhase{Phase: "listening"})
		<-ctx.Done()
		return "", ctx.Err()
	}, func(context.Context) error { played = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || !played {
		t.Fatalf("stalled control err=%v played=%t", err, played)
	}
	select {
	case <-joined:
	default:
		t.Fatal("stalled turn left running")
	}
}
