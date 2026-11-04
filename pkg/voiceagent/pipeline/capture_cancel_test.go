package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

func TestCaptureCancellationBeforeSessionAdoption(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &boundarySession{}
	p := &Pipeline{
		STT: boundaryProvider{start: func(context.Context) (stt.Session, error) {
			// Model an adapter that opens its reader just as its caller cancels.
			cancel()
			return reader, nil
		}},
		Brain:  &fakeBrain{chunks: []string{"Next turn works."}},
		Events: events.NewBus(),
	}
	metrics := countTurnMetrics(p.Events)
	if _, err := p.RunTurn(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, ErrInputTransition) {
		t.Fatalf("canceled start = %v, want cancellation without binding transition", err)
	}
	if !reader.closed.Load() || metrics.Outcome() != "interrupted" {
		t.Fatalf("closed=%v outcome=%s", reader.closed.Load(), metrics.Outcome())
	}
	p.STT = &fakeSTT{text: "next input"}
	if text, err := p.RunTurn(t.Context()); err != nil || text != "next input" {
		t.Fatalf("next turn = %q, %v", text, err)
	}
	if metrics.Load() != 2 || metrics.Outcome() != "completed" {
		t.Fatalf("terminal count=%d outcome=%s", metrics.Load(), metrics.Outcome())
	}
}

type cancelOnResumeSession struct {
	*captureReuseSession
	cancel context.CancelFunc
}

func (s *cancelOnResumeSession) ResumeCapture(context.Context, stt.PauseToken) error {
	s.cancel()
	return nil
}

func TestCaptureCancellationAfterResumeReleasesReader(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelOnResumeSession{captureReuseSession: newCaptureReuseSession(), cancel: cancel}
	capture := &resetCapture{}
	p := &Pipeline{STT: idleBoundaryProvider(), Capture: capture, captureSession: reader, captureGeneration: 1, captureYielded: true}
	p.keepCapture.Store(true)
	if _, _, err := p.startCaptureSession(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, ErrInputTransition) {
		t.Fatalf("canceled resume = %v, want cancellation without binding transition", err)
	}
	if reader.closes.Load() != 1 {
		t.Fatalf("resumed reader closed %d times", reader.closes.Load())
	}
	if p.keepCapture.Load() {
		t.Fatal("canceled resume left the transferred retention claim set")
	}
	session, generation, err := p.startCaptureSession(t.Context())
	if err != nil {
		t.Fatalf("next reader remained fenced: %v", err)
	}
	p.finishCaptureSession(session, generation, false)
	if got := capture.resets.Load(); got != 1 {
		t.Fatalf("next fresh reader reset capture %d times, want 1", got)
	}
}
