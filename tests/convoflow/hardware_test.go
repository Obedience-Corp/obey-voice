package main

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/aecprobe"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type acousticReadinessSTT struct {
	events   chan stt.Event
	started  chan struct{}
	closed   atomic.Bool
	startErr error
}

func (*acousticReadinessSTT) Available() bool { return true }
func (s *acousticReadinessSTT) Start(context.Context) (stt.Session, error) {
	close(s.started)
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s, nil
}
func (s *acousticReadinessSTT) Events() <-chan stt.Event { return s.events }
func (s *acousticReadinessSTT) Close() error             { s.closed.Store(true); return nil }

func TestAcousticTurnWaitsForProductionListeningBeforePlayback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := &acousticReadinessSTT{events: make(chan stt.Event, 2), started: make(chan struct{})}
	p := &pipeline.Pipeline{STT: s, Events: events.NewBus()}
	var listening, played atomic.Bool
	remove := p.Events.SubscribeAll(func(e events.Event) {
		if phase, ok := e.(events.STTPhase); ok && phase.Phase == "listening" {
			listening.Store(true)
		}
	})
	defer remove()
	done := make(chan error, 1)
	go func() {
		_, err := runAcousticTurn(ctx, p.Events, p.RunTurn, func(context.Context) error {
			if !listening.Load() {
				return errors.New("playback preceded production listening event")
			}
			played.Store(true)
			s.events <- stt.Timeout{}
			return nil
		})
		done <- err
	}()
	select {
	case <-s.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if played.Load() {
		t.Error("stimulus played while STT had not reported listening")
	}
	// Only STT input is controlled. Pipeline.RunTurn must consume and forward
	// the phase to its actual bus before the physical-playback callback runs.
	s.events <- stt.PhaseEvent{Phase: "listening"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !played.Load() || !s.closed.Load() {
		t.Fatal("playback or joined STT cleanup missing")
	}
}

func TestAcousticTurnDoesNotPlayAfterEarlyFailureOrCancellation(t *testing.T) {
	for _, mode := range []string{"early_failure", "cancel_before_listening", "playback_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			expected := errors.New("fixture failure")
			s := &acousticReadinessSTT{events: make(chan stt.Event, 1), started: make(chan struct{})}
			if mode == "early_failure" {
				s.startErr = expected
			}
			if mode == "playback_failure" {
				s.events <- stt.PhaseEvent{Phase: "listening"}
			}
			p := &pipeline.Pipeline{STT: s, Events: events.NewBus()}
			var played atomic.Bool
			done := make(chan error, 1)
			go func() {
				_, err := runAcousticTurn(ctx, p.Events, p.RunTurn, func(context.Context) error { played.Store(true); return expected })
				done <- err
			}()
			select {
			case <-s.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if mode == "cancel_before_listening" {
				cancel()
				expected = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, expected) {
					t.Fatalf("error=%v, want %v", err, expected)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("readiness failure did not cancel and join turn")
			}
			if played.Load() != (mode == "playback_failure") {
				t.Fatalf("unexpected playback=%t", played.Load())
			}
			if mode != "early_failure" && !s.closed.Load() {
				t.Fatal("returned before STT session closed")
			}
		})
	}
}

func TestNormalizeAcousticTextIgnoresCaseAndPunctuation(t *testing.T) {
	if got := normalizeAcousticText("  Hello, Samantha! "); got != "hello samantha" {
		t.Fatalf("normalized transcript = %q", got)
	}
	if normalizeAcousticText("hello samantha") != normalizeAcousticText("HELLO, SAMANTHA.") {
		t.Fatal("equivalent transcript spellings did not normalize equally")
	}
}

func TestAcousticRunRejectsUnsafeInputsBeforeOpeningDevices(t *testing.T) {
	if err := (*liveRunner)(nil).runAcoustic(context.Background(), "speech.wav", "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent is not configured") {
		t.Fatalf("nil runner error = %v", err)
	}
}

func TestAcousticEnvelopeCorrelationRejectsSilenceAndClipping(t *testing.T) {
	stimulus := make([]float32, 4*audio.ChunkSize)
	for i := range stimulus {
		stimulus[i] = float32((i/audio.ChunkSize)%4+1) * 0.04
	}
	if got := acousticEnvelopeCorrelation(stimulus, make([]float32, len(stimulus)), audio.SampleRate/100, 20); got != 0 {
		t.Fatalf("silence envelope correlation = %.3f", got)
	}
	if peak := aecprobe.PeakLevel([]float32{0.2, 1.0}); peak < 0.999 {
		t.Fatalf("clipping peak = %.3f", peak)
	}
}

func acousticScoringFixture() []float32 {
	// A varying envelope gives alignment a unique maximum. Include a partial
	// final 10ms frame so completeness cannot be inferred from envelope length.
	samples := make([]float32, 80*(audio.SampleRate/100)+79)
	for i := range samples {
		frame := i / (audio.SampleRate / 100)
		value := float32((frame*frame+3*frame)%17+1) * 0.004
		if i%2 == 1 {
			value = -value
		}
		samples[i] = value
	}
	return samples
}

func TestAcousticStimulusWindowIgnoresTrailingQuiet(t *testing.T) {
	fixture := acousticScoringFixture()
	delay := audio.SampleRate / 2
	short := append(make([]float32, delay), fixture...)
	long := append(append([]float32(nil), short...), make([]float32, 30*audio.SampleRate)...)
	var a, b acousticReport
	if err := scoreAcousticCapture(&a, fixture, short); err != nil {
		t.Fatal(err)
	}
	if err := scoreAcousticCapture(&b, fixture, long); err != nil {
		t.Fatal(err)
	}
	if a.StimulusWindowStart != delay || a.StimulusWindowEnd != len(short) || !a.StimulusWindowValid {
		t.Fatalf("unexpected fixture window: %+v", a)
	}
	if b.StimulusWindowStart != a.StimulusWindowStart || b.StimulusWindowEnd != a.StimulusWindowEnd || b.StimulusWindowRMS != a.StimulusWindowRMS {
		t.Fatalf("trailing silence changed window: short=%+v long=%+v", a, b)
	}
	if math.Abs(a.StimulusWindowRMS-aecprobe.SegmentRMS(fixture)) > 1e-9 || b.CapturedRMS >= a.CapturedRMS || !b.StimulusAudible {
		t.Fatalf("fixture RMS not preserved independently of whole capture: %+v", b)
	}
}

func TestAcousticStimulusWindowRejectsQuietFixtureDespiteLaterResponse(t *testing.T) {
	fixture := acousticScoringFixture()
	delay := audio.SampleRate / 2
	mic := make([]float32, 6*audio.SampleRate)
	for i, sample := range fixture {
		mic[delay+i] = sample / 32
	}
	// The response is later than the allowed two-second fixture onset search.
	// Even a loud response with the same envelope must not satisfy audibility.
	for i, sample := range fixture {
		mic[4*audio.SampleRate+i] = sample * 4
	}
	var report acousticReport
	err := scoreAcousticCapture(&report, fixture, mic)
	if err == nil || !strings.Contains(err.Error(), "stimulus window is not audible") {
		t.Fatalf("quiet fixture accepted: report=%+v error=%v", report, err)
	}
	if report.CapturedRMS <= report.StimulusWindowRMS || report.StimulusAudible || report.StimulusWindowStart != delay {
		t.Fatalf("wrong audio supplied stimulus score: %+v", report)
	}
}

func TestAcousticStimulusWindowUsesProductionAudibilityAcrossPauses(t *testing.T) {
	fixture := make([]float32, audio.SampleRate)
	for i := audio.SampleRate / 10; i < 2*audio.SampleRate/10; i++ {
		fixture[i] = 0.003
		if i%2 == 1 {
			fixture[i] = -fixture[i]
		}
	}
	for _, tc := range []struct {
		name string
		gain float32
		want bool
	}{
		{"audible_word_survives_pause", 1, true},
		{"entire_window_below_microphone_floor", 0.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mic := make([]float32, len(fixture))
			for i, sample := range fixture {
				mic[i] = sample * tc.gain
			}
			var report acousticReport
			err := scoreAcousticCapture(&report, fixture, mic)
			if (err == nil) != tc.want || report.StimulusAudible != tc.want {
				t.Fatalf("audibility=%t want=%t error=%v", report.StimulusAudible, tc.want, err)
			}
			if report.StimulusWindowRMS >= 0.002 {
				t.Fatalf("fixture must demonstrate dilution below STT's microphone floor: %+v", report)
			}
		})
	}
}

func TestAcousticStimulusWindowRejectsIncompleteOrUnlocatableCapture(t *testing.T) {
	fixture := acousticScoringFixture()
	for _, tc := range []struct {
		name string
		mic  []float32
	}{
		{"missing_partial_frame_tail", fixture[:len(fixture)-1]},
		{"silence", make([]float32, len(fixture))},
		{"after_onset_search", append(make([]float32, 3*audio.SampleRate), fixture...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report acousticReport
			if err := scoreAcousticCapture(&report, fixture, tc.mic); err == nil || report.StimulusWindowValid {
				t.Fatalf("invalid fixture window accepted: report=%+v error=%v", report, err)
			}
		})
	}
}

func TestAcousticEnvelopeCorrelationSeparatesRecordedFixtures(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_BENCH_FIXTURES")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("home directory unavailable")
		}
		root = filepath.Join(home, ".cache", "obey-voice", "fixtures", "bench")
	}
	paths := make([][]float32, 3)
	for i := range paths {
		source, err := audio.NewFixtureSourceFromWAV(filepath.Join(root, "synth-0"+string(rune('1'+i))+".wav"), audio.ChunkSize, false)
		if err != nil {
			t.Skipf("recorded benchmark fixtures unavailable: %v", err)
		}
		for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
			paths[i] = append(paths[i], chunk...)
		}
		_ = source.Close()
	}
	good := acousticEnvelopeCorrelation(paths[0], append(make([]float32, audio.SampleRate/2), paths[0]...), audio.SampleRate/100, 100)
	if good < acousticMinEnvelopeCorrelation {
		t.Fatalf("same fixture correlation = %.3f, want >= %.3f", good, acousticMinEnvelopeCorrelation)
	}
	for i := 1; i < len(paths); i++ {
		common := min(len(paths[0]), len(paths[i]))
		if got := acousticEnvelopeCorrelation(paths[0][:common], paths[i][:common], audio.SampleRate/100, 100); got >= acousticMinEnvelopeCorrelation {
			t.Fatalf("wrong fixture %d correlation = %.3f, want < %.3f", i+1, got, acousticMinEnvelopeCorrelation)
		}
	}
}

func TestAcousticTranscriptNumberFormattingPreservesMeaning(t *testing.T) {
	if normalizeAcousticText("What is two plus two?") != normalizeAcousticText("What is 2 plus 2?") {
		t.Fatal("equivalent spoken numbers differ")
	}
	for _, wrong := range []string{"What is two plus three?", "What is not two plus two?", "What is two?"} {
		if normalizeAcousticText(wrong) == normalizeAcousticText("What is two plus two?") {
			t.Fatalf("changed meaning accepted: %q", wrong)
		}
	}
}

// An opt-in offline check verifies that the saved audio reproduces the gate's
// score. It never opens a device or starts an inference provider.
func TestAcousticSavedCaptureReproducesScore(t *testing.T) {
	dir := os.Getenv("OBEY_VOICE_ACOUSTIC_CAPTURE")
	if dir == "" {
		t.Skip("set OBEY_VOICE_ACOUSTIC_CAPTURE to an acoustic artifact directory")
	}
	source, err := audio.NewFixtureSourceFromWAV(filepath.Join(dir, "stimulus.wav"), audio.ChunkSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var reference []float32
	for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
		reference = append(reference, chunk...)
	}
	mic, rate, err := audio.ReadWAVFloat32(filepath.Join(dir, "mic-in.wav"))
	if err != nil || rate != audio.SampleRate {
		t.Fatalf("microphone rate=%d err=%v", rate, err)
	}
	var report acousticReport
	err = scoreAcousticCapture(&report, reference, mic)
	t.Logf("saved capture: whole_rms=%.6f window=[%d,%d) window_rms=%.6f audible=%t envelope_correlation=%.6f", report.CapturedRMS, report.StimulusWindowStart, report.StimulusWindowEnd, report.StimulusWindowRMS, report.StimulusAudible, report.EnvelopeCorrelation)
	if err != nil {
		t.Fatal(err)
	}
}
