package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

var ErrCaptureTeardown = errors.New("capture teardown failed; restart voice mode before recording again")

var ErrInputTransition = errors.New("capture is changing bindings; retry after the reader stops")

type captureStartup struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// InputBinding is one response-boundary input choice. Native input and Standard
// recognition are exclusive; the conversation host owns their resource lifetime.
type InputBinding struct {
	TextOnly bool
	Input    utterance.Provider
	STT      stt.Provider
	Native   *NativePolicy
}

// PreparedInputReplacement holds capture closed until publication or rollback.
// It complements PreparedBrainReplacement at the host's serialized boundary.
// A failed join never permits a second reader over the same source.
type PreparedInputReplacement struct {
	pipeline *Pipeline
	next     InputBinding
	failure  error
}

func (p *Pipeline) PrepareInputReplacement(ctx context.Context, next InputBinding) (*PreparedInputReplacement, error) {
	if p == nil {
		return nil, errors.New("input replacement requires pipeline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateInputBinding(next); err != nil {
		return nil, err
	}
	prepared := &PreparedInputReplacement{pipeline: p, next: next}
	p.captureMu.Lock()
	if p.captureBoundary != nil {
		err := p.captureBoundary.transitionError()
		p.captureMu.Unlock()
		return nil, err
	}
	p.captureBoundary = prepared
	if p.audioMedia == nil && p.Native != nil {
		p.audioMedia = p.Native.Media
	}
	session, startup, cancel := p.captureSession, p.captureStarting, p.captureCancel
	p.captureGeneration++
	p.captureSession = nil
	p.captureCancel = nil
	p.captureYielded = false
	p.captureToken = stt.PauseToken{}
	p.captureMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if startup != nil {
		startup.cancel()
	}
	p.keepCapture.Store(false)
	p.clearPreparedCapture()
	done := make(chan error, 1)
	go func() {
		var err error
		if session != nil {
			err = session.Close()
		}
		if startup != nil {
			<-startup.done
		}
		done <- err
	}()
	joinCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	select {
	case err := <-done:
		if err != nil {
			return nil, prepared.latchFailure(err)
		} // keep admission closed until host replaces this capture owner
		if err := ctx.Err(); err != nil {
			prepared.Discard()
			return nil, err
		}
		if p.Capture != nil {
			p.Capture.Reset()
		}
		return prepared, nil
	case <-joinCtx.Done():
		// The old binding remains installed. Resume admission only after its reader
		// eventually confirms successful teardown; never race a replacement reader.
		go func() {
			if err := <-done; err == nil {
				prepared.Discard()
			} else {
				_ = prepared.latchFailure(err)
			}
		}()
		return nil, joinCtx.Err()
	}
}

// Validate must run before persistence. Publish is deliberately infallible after
// durable success; the host serializes all lifecycle transitions around it.
func (r *PreparedInputReplacement) Validate() error {
	if r == nil || r.pipeline == nil {
		return ErrInputTransition
	}
	r.pipeline.captureMu.Lock()
	defer r.pipeline.captureMu.Unlock()
	if r.pipeline.captureBoundary != r {
		return ErrInputTransition
	}
	return nil
}
func (r *PreparedInputReplacement) Publish() {
	if r == nil || r.pipeline == nil {
		return
	}
	p := r.pipeline
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.captureBoundary != r {
		return
	}
	p.Input, p.STT, p.Native = r.next.Input, r.next.STT, r.next.Native
	if r.next.Native != nil {
		p.audioMedia = r.next.Native.Media
	}
	p.captureBoundary = nil
}
func (r *PreparedInputReplacement) Discard() {
	if r == nil || r.pipeline == nil {
		return
	}
	p := r.pipeline
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.captureBoundary == r {
		p.captureBoundary = nil
	}
}

func validateInputBinding(next InputBinding) error {
	if next.TextOnly {
		if next.Input != nil || next.STT != nil {
			return errors.New("text input cannot own capture")
		}
		if next.Native != nil && next.Native.Media == nil {
			return errors.New("native text input requires media binding")
		}
		return nil
	}
	if next.Input != nil {
		if next.STT != nil || next.Native == nil || next.Native.Media == nil || !next.Input.Available() {
			return errors.New("native input requires an exclusive available media binding")
		}
	} else if next.Native != nil || next.STT == nil || !next.STT.Available() {
		return errors.New("standard input requires an available recognizer")
	}
	return nil
}

// Stage changes only the candidate while capture admission remains closed. It
// lets the host quiesce the old reader before doing quiet setup/model checks.
func (r *PreparedInputReplacement) Stage(next InputBinding) error {
	if err := validateInputBinding(next); err != nil {
		return err
	}
	if r == nil || r.pipeline == nil {
		return ErrInputTransition
	}
	p := r.pipeline
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.captureBoundary != r {
		return ErrInputTransition
	}
	if next.Native != nil && p.audioMedia != nil && next.Native.Media != p.audioMedia {
		return ErrAudioMediaOwner
	}
	r.next = next
	return nil
}

// InputSnapshot is a borrowed snapshot for the serialized lifecycle owner.
func (p *Pipeline) InputSnapshot(textOnly bool) InputBinding {
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	return InputBinding{Input: p.Input, STT: p.STT, Native: p.Native, TextOnly: textOnly}
}

// transitionError is read while captureMu is held so every later recording
// attempt retains the actionable reason for a permanently unverified teardown.
func (r *PreparedInputReplacement) transitionError() error {
	if r.failure != nil {
		return r.failure
	}
	return ErrInputTransition
}

func (r *PreparedInputReplacement) latchFailure(err error) error {
	failure := fmt.Errorf("%w: %w", ErrCaptureTeardown, err)
	r.pipeline.captureMu.Lock()
	r.failure = failure
	r.pipeline.captureMu.Unlock()
	return failure
}
