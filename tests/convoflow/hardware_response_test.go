package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

func TestEchoOnlyResponseRejectsMutedPhysicalPlayerBeforeDeviceOpen(t *testing.T) {
	for _, gain := range []float64{0, .004, 1} {
		player := playback.NewPlayerWithDevice("not opened by this test")
		player.SetVolume(gain)
		if err := requireEchoResponseVolume(player); (err == nil) != (gain == 1) {
			t.Fatalf("configured gain=%g effective=%g err=%v", gain, player.Volume(), err)
		}
		if err := player.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type responseTestFrames struct{ frames []audio.Frame }

func (s *responseTestFrames) ReadFrame(context.Context) (audio.Frame, error) {
	if len(s.frames) == 0 {
		return audio.Frame{}, audio.ErrNoFrameReady
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}
func (*responseTestFrames) Close() error       { return nil }
func (*responseTestFrames) InputGain() float64 { return 6 }

func TestEchoOnlyResponseCaptureRejectsLossAndReopen(t *testing.T) {
	for _, mode := range []string{"ordinary_reset", "overflow", "reopen"} {
		t.Run(mode, func(t *testing.T) {
			first := audio.Frame{Samples: []float32{.1}, CaptureSpans: []audio.CaptureSpan{{DeviceGeneration: 1}}}
			next := audio.Frame{Samples: []float32{.2}, CaptureSpans: []audio.CaptureSpan{{DeviceGeneration: 1}}, CaptureGap: audio.CaptureGap{Reset: true, DiscardedSamples: 20}}
			if mode == "overflow" {
				next.CaptureGap.OverflowSamples = 2
			}
			if mode == "reopen" {
				next.CaptureSpans[0].DeviceGeneration = 2
			}
			frames := &responseTestFrames{frames: []audio.Frame{first, next}}
			source := &responseCaptureSource{FrameSource: frames, gain: frames}
			if _, err := source.ReadFrame(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, err := source.ReadFrame(t.Context())
			if (err == nil) != (mode == "ordinary_reset") {
				t.Fatalf("loss/reopen result: %v", err)
			}
			if source.InputGain() != 6 {
				t.Fatal("capture gain lost")
			}
			want := int64(1)
			if mode == "ordinary_reset" {
				want = 2
			}
			if source.readSamples.Load() != want {
				t.Fatalf("invalid frames counted: %d", source.readSamples.Load())
			}
		})
	}
}

func TestEchoOnlyResponseRequiresCompletedPlaybackAndHealthyNextListen(t *testing.T) {
	for _, mode := range []string{"pass", "wrong_initial", "self_interrupt", "missing_completion", "missing_metrics", "degraded", "playback_failure", "provider_error", "early_stt_timeout", "quiet_user_input", "quiet_admission", "quiet_hardware_error", "quiet_deadline", "quiet_stall", "cancel_and_hardware_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			bus := events.NewBus()
			played := make(chan struct{})
			var calls, joined, sampleReads atomic.Int64
			report := acousticResponseReport{Expected: "Count slowly.", QuietDuration: 5 * time.Millisecond}
			err := runEchoOnlyResponse(ctx, bus, func(ctx context.Context) (string, error) {
				call := calls.Add(1)
				defer joined.Add(1)
				bus.Emit(events.STTPhase{Phase: "listening"})
				if call == 1 {
					select {
					case <-played:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					input := report.Expected
					if mode == "wrong_initial" {
						input = "lost opening words"
					}
					bus.Emit(events.UserInput{Text: input})
					if mode == "wrong_initial" {
						<-ctx.Done()
						return input, ctx.Err()
					}
					if mode == "provider_error" {
						return input, errors.New("model unavailable")
					}
					bus.Emit(events.SpeakingStarted{AudibleSink: "local_speaker"})
					if mode == "self_interrupt" {
						bus.Emit(events.SpeakingInterrupted{Reason: "barge_in"})
						<-ctx.Done()
						return input, ctx.Err()
					}
					if mode != "missing_completion" {
						bus.Emit(events.SpeakingComplete{AudibleSink: "local_speaker", Elapsed: time.Second})
					}
					bus.Emit(events.ResponseReady{Degraded: mode == "degraded"})
					if mode != "missing_metrics" {
						bus.Emit(events.TurnMetrics{Outcome: "completed", AudibleSink: "local_speaker"})
					}
					return input, nil
				}
				switch mode {
				case "early_stt_timeout":
					bus.Emit(events.TurnMetrics{Outcome: "timed_out"})
					return "", nil
				case "quiet_user_input":
					bus.Emit(events.UserInput{Text: "assistant echo"})
				case "quiet_admission":
					bus.Emit(events.STTPhase{Phase: "hearing"})
				case "quiet_hardware_error":
					return "", errors.New("microphone disconnected")
				case "quiet_deadline":
					return "", context.DeadlineExceeded
				}
				<-ctx.Done()
				if mode == "cancel_and_hardware_error" {
					return "", errors.Join(ctx.Err(), errors.New("late device error"))
				}
				return "", ctx.Err()
			}, func(context.Context) error {
				close(played)
				if mode == "playback_failure" {
					return errors.New("cannot open speaker")
				}
				return nil
			}, func() int64 {
				if mode == "quiet_stall" {
					return 0
				}
				return sampleReads.Add(16000)
			}, &report)
			if (err == nil) != (mode == "pass") || report.NoEchoObserved != (mode == "pass") {
				t.Fatalf("err=%v report=%+v", err, report)
			}
			if joined.Load() != calls.Load() {
				t.Fatalf("returned before turns joined: calls=%d joined=%d", calls.Load(), joined.Load())
			}
			if mode == "pass" && (calls.Load() != 2 || report.ResponseSegments != 1 || report.QuietEnded.Sub(report.QuietStarted) < report.QuietDuration) {
				t.Fatalf("missing measured response/listen evidence: calls=%d report=%+v", calls.Load(), report)
			}
			if mode == "quiet_user_input" && report.UnexpectedInput != "assistant echo" {
				t.Fatalf("unexpected text lost: %+v", report)
			}
			old := report.UnexpectedInput
			bus.Emit(events.UserInput{Text: "later unrelated turn"})
			if report.UnexpectedInput != old {
				t.Fatal("control observer leaked into later turn")
			}
		})
	}
}

func TestEchoOnlyResponseOuterDeadlineCannotPassAsSilence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	bus := events.NewBus()
	report := acousticResponseReport{QuietDuration: time.Hour}
	joined := make(chan struct{})
	err := observeQuietNextTurn(ctx, bus, func(ctx context.Context) (string, error) {
		defer close(joined)
		bus.Emit(events.STTPhase{Phase: "listening"})
		cancel()
		<-ctx.Done()
		return "", ctx.Err()
	}, func() int64 { return 1000000 }, &report)
	if err == nil || report.NoEchoObserved {
		t.Fatalf("cancellation passed as silence: %v %+v", err, report)
	}
	select {
	case <-joined:
	default:
		t.Fatal("canceled turn was not joined")
	}
}

func TestEchoOnlyResponseQuietWindowJoinsProductionPipelineCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	session := &acousticReadinessSTT{events: make(chan stt.Event, 1), started: make(chan struct{})}
	session.events <- stt.PhaseEvent{Phase: "listening"}
	bus := events.NewBus()
	p := &pipeline.Pipeline{STT: session, Events: bus}
	var reads atomic.Int64
	report := acousticResponseReport{QuietDuration: 5 * time.Millisecond}
	if err := observeQuietNextTurn(ctx, bus, p.RunTurn, func() int64 { return reads.Add(16000) }, &report); err != nil {
		t.Fatal(err)
	}
	if !report.NoEchoObserved || !session.closed.Load() {
		t.Fatalf("production cancellation did not join retained STT: report=%+v closed=%t", report, session.closed.Load())
	}
}

type acousticResponseRunner struct {
	fakeRunner
	wav, expected, artifacts string
	quiet                    time.Duration
}

func (r *acousticResponseRunner) runAcousticResponse(_ context.Context, wav, expected, artifacts string, quiet time.Duration) error {
	r.wav, r.expected, r.artifacts, r.quiet = wav, expected, artifacts, quiet
	return nil
}

func TestAcousticResponseScenarioAndEngineForwardKnownFixture(t *testing.T) {
	body := "name: response\ninput: speech\ncapture: {microphone: true, speakers: true}\nturns:\n  - id: response\n    op: acoustic_response\n    wav: counting.wav\n    expected_transcript: Count slowly.\n    duration: 3s\n"
	scn, err := loadScenarioString("response", body)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	r := &acousticResponseRunner{fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true}}
	e := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: r}
	if err := e.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.wav != "counting.wav" || r.expected != "Count slowly." || r.artifacts != filepath.Join(e.runDir, "acoustic", "response") || r.quiet != 3*time.Second {
		t.Fatalf("incorrect response control arguments: %+v", r)
	}
	for _, invalid := range []string{
		strings.Replace(body, "duration: 3s", "duration: 0s", 1),
		strings.Replace(body, "    wav: counting.wav\n", "", 1),
		strings.Replace(body, "    expected_transcript: Count slowly.\n", "", 1),
		strings.Replace(body, "microphone: true", "microphone: false", 1),
		body + "    interrupt_wav: unexpected.wav\n",
	} {
		if _, err := loadScenarioString("invalid", invalid); err == nil {
			t.Fatalf("invalid acoustic_response accepted: %s", invalid)
		}
	}
}
