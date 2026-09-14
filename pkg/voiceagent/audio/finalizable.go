package audio

import (
	"context"
	"sync/atomic"
)

// Finalizable wraps a live FrameSource so a caller can end the utterance being
// read on demand — the local-microphone twin of Ingress.Finalize. A talk key
// released, a "send" tap: the recogniser gets one Final frame on its next read
// and commits what it heard instead of waiting out the silence timeout.
//
// The request is one-shot: it is consumed by the read that honours it, and a
// turn that finishes for any other reason clears it, so a release that lands
// between turns cannot end the next one before it starts.
type Finalizable struct {
	src     FrameSource
	pending atomic.Bool
}

// NewFinalizable wraps src. The wrapper owns nothing: Close closes src.
func NewFinalizable(src FrameSource) *Finalizable {
	return &Finalizable{src: src}
}

// InputGain preserves the source's current amplification for STT's microphone
// audibility floor. Read it on demand because front-end gain can change while
// an utterance is being captured.
func (f *Finalizable) InputGain() float64 {
	if reporter, ok := f.src.(GainReporter); ok {
		return reporter.InputGain()
	}
	return 1
}

// Finalize asks for the current utterance to end. The next ReadFrame returns a
// Final frame; frames the source already buffered are dropped, which is what
// "I have finished speaking" means.
func (f *Finalizable) Finalize() {
	f.pending.Store(true)
}

// Clear drops a pending Finalize that no read consumed.
func (f *Finalizable) Clear() {
	f.pending.Store(false)
}

// Pending reports whether a Finalize is waiting for a read.
func (f *Finalizable) Pending() bool {
	return f.pending.Load()
}

// ReadFrame implements FrameSource.
func (f *Finalizable) ReadFrame(ctx context.Context) (Frame, error) {
	if f.pending.Swap(false) {
		return Frame{SourceKind: SourceLive, Final: true}, nil
	}
	return f.src.ReadFrame(ctx)
}

// Read implements the legacy audioSource contract for providers that still
// poll samples. That path cannot see a Final frame, so a pending Finalize only
// starves it of samples until the frame path picks it up.
func (f *Finalizable) Read() []float32 {
	if f.pending.Load() {
		return nil
	}
	if r, ok := f.src.(interface{ Read() []float32 }); ok {
		return r.Read()
	}
	frame, err := f.src.ReadFrame(context.Background())
	if err != nil || frame.Final {
		return nil
	}
	return frame.Samples
}

// Close closes the wrapped source.
func (f *Finalizable) Close() error {
	return f.src.Close()
}

var _ FrameSource = (*Finalizable)(nil)
var _ GainReporter = (*Finalizable)(nil)
