package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type boundarySession struct {
	closeErr     error
	closeStarted chan struct{}
	release      chan struct{}
	once         sync.Once
	closed       atomic.Bool
}

func (s *boundarySession) Events() <-chan stt.Event { return nil }
func (s *boundarySession) Close() error {
	s.once.Do(func() {
		if s.closeStarted != nil {
			close(s.closeStarted)
		}
	})
	if s.release != nil {
		<-s.release
	}
	s.closed.Store(true)
	return s.closeErr
}

type boundaryProvider struct {
	start func(context.Context) (stt.Session, error)
}

func (p boundaryProvider) Start(ctx context.Context) (stt.Session, error) { return p.start(ctx) }
func (boundaryProvider) Available() bool                                  { return true }
func idleBoundaryProvider() boundaryProvider {
	return boundaryProvider{start: func(context.Context) (stt.Session, error) { return &boundarySession{}, nil }}
}

func TestPreparedInputWaitsForReaderBeforePublicationAndRollback(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "publish"}[commit], func(t *testing.T) {
			var oldStarts, newStarts atomic.Int32
			old := boundaryProvider{start: func(context.Context) (stt.Session, error) { oldStarts.Add(1); return &boundarySession{}, nil }}
			newInput := boundaryProvider{start: func(context.Context) (stt.Session, error) { newStarts.Add(1); return &boundarySession{}, nil }}
			reader := &boundarySession{closeStarted: make(chan struct{}), release: make(chan struct{})}
			p := &Pipeline{STT: old, captureSession: reader, captureGeneration: 1, captureYielded: true}
			ready := make(chan *PreparedInputReplacement, 1)
			errs := make(chan error, 1)
			go func() {
				next, err := p.PrepareInputReplacement(t.Context(), InputBinding{STT: newInput})
				ready <- next
				errs <- err
			}()
			<-reader.closeStarted
			if _, _, err := p.startCaptureSession(t.Context()); !errors.Is(err, ErrInputTransition) {
				t.Fatal("second reader admitted", err)
			}
			select {
			case <-ready:
				t.Fatal("published before old reader stopped")
			default:
			}
			close(reader.release)
			prepared := <-ready
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if !reader.closed.Load() {
				t.Fatal("old reader still active")
			}
			if err := prepared.Validate(); err != nil {
				t.Fatal(err)
			}
			if commit {
				prepared.Publish()
			} else {
				prepared.Discard()
			}
			// Standard provider values contain functions and are not comparable; verify
			// usable ownership by opening exactly one reader after either outcome.
			session, generation, err := p.startCaptureSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p.finishCaptureSession(session, generation, false)
			if commit && (newStarts.Load() != 1 || oldStarts.Load() != 0) {
				t.Fatal("published input did not own reader")
			}
			if !commit && (oldStarts.Load() != 1 || newStarts.Load() != 0) {
				t.Fatal("rollback did not preserve old input")
			}
			if err := prepared.Validate(); !errors.Is(err, ErrInputTransition) {
				t.Fatal("finished lease reusable")
			}
		})
	}
}

func TestPreparedInputCancellationKeepsLateStartingReaderFenced(t *testing.T) {
	started, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	session := &boundarySession{closeStarted: closed}
	provider := boundaryProvider{start: func(context.Context) (stt.Session, error) { close(started); <-release; return session, nil }}
	p := &Pipeline{STT: provider}
	startResult := make(chan error, 1)
	go func() { _, _, err := p.startCaptureSession(t.Context()); startResult <- err }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	boundaryResult := make(chan error, 1)
	go func() {
		_, err := p.PrepareInputReplacement(ctx, InputBinding{STT: idleBoundaryProvider()})
		boundaryResult <- err
	}()
	// Wait on state, not an arbitrary delay: the replacement must own the fence
	// before cancellation can exercise its late-start cleanup.
	deadline := time.After(time.Second)
	for {
		p.captureMu.Lock()
		held := p.captureBoundary != nil
		p.captureMu.Unlock()
		if held {
			break
		}
		select {
		case <-deadline:
			t.Fatal("boundary did not acquire capture")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-boundaryResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := p.startCaptureSession(t.Context()); !errors.Is(err, ErrInputTransition) {
		t.Fatal("second reader admitted before late start joined", err)
	}
	close(release)
	if err := <-startResult; !errors.Is(err, ErrInputTransition) {
		t.Fatal(err)
	}
	<-closed
	if !session.closed.Load() {
		t.Fatal("late finalized session was abandoned")
	}
	// Successful late teardown eventually reopens the unchanged binding.
	deadline = time.After(time.Second)
	for {
		p.captureMu.Lock()
		held := p.captureBoundary != nil
		p.captureMu.Unlock()
		if !held {
			break
		}
		select {
		case <-deadline:
			t.Fatal("old binding never became available")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestPreparedInputCloseFailureLatchesActionableAdmissionError(t *testing.T) {
	cause := errors.New("fixture reader could not stop")
	old := idleBoundaryProvider()
	p := &Pipeline{STT: old, captureSession: &boundarySession{closeErr: cause}}
	if _, err := p.PrepareInputReplacement(t.Context(), InputBinding{STT: idleBoundaryProvider()}); !errors.Is(err, ErrCaptureTeardown) || !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, _, err := p.startCaptureSession(t.Context()); !errors.Is(err, ErrCaptureTeardown) {
		t.Fatal("retry lost actionable failure", err)
	}
}
