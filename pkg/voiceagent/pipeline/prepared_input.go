package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

var ErrInputTransition = errors.New("capture is changing bindings; retry after the reader stops")

type captureStartup struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// InputBinding is one response-boundary input choice. Native input and Standard
// recognition are exclusive; the conversation host owns their resource lifetime.
type InputBinding struct {
	Input  utterance.Provider
	STT    stt.Provider
	Native *NativePolicy
}

// PreparedInputReplacement holds capture closed until publication or rollback.
// It complements PreparedBrainReplacement at the host's serialized boundary.
// A failed join never permits a second reader over the same source.
type PreparedInputReplacement struct {
	pipeline *Pipeline
	next     InputBinding
}

func (p *Pipeline) PrepareInputReplacement(ctx context.Context, next InputBinding) (*PreparedInputReplacement, error) {
	if p == nil {
		return nil, errors.New("input replacement requires pipeline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if next.Input != nil {
		if next.STT != nil || next.Native == nil || next.Native.Media == nil || !next.Input.Available() {
			return nil, errors.New("native input requires an exclusive available media binding")
		}
	} else if next.Native != nil || next.STT == nil || !next.STT.Available() {
		return nil, errors.New("standard input requires an available recognizer")
	}
	prepared := &PreparedInputReplacement{pipeline: p, next: next}
	p.captureMu.Lock()
	if p.captureBoundary != nil {
		p.captureMu.Unlock()
		return nil, ErrInputTransition
	}
	p.captureBoundary = prepared
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
			return nil, err
		} // keep closed if reader teardown was not verified
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
