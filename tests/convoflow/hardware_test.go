package main

import (
	"context"
	"errors"
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

func TestAcousticEnvelopeCorrelationSeparatesRecordedFixtures(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_BENCH_FIXTURES")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("home directory unavailable")
		}
		root = filepath.Join(home, ".cache", "festival-voice", "fixtures", "bench")
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
	score := acousticEnvelopeCorrelation(reference, mic, audio.SampleRate/100, 200)
	t.Logf("saved microphone envelope correlation %.6f", score)
	if score < acousticMinEnvelopeCorrelation {
		t.Fatalf("saved capture confidence %.6f below %.2f", score, acousticMinEnvelopeCorrelation)
	}
}
