package audio

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrStreamStalled reports that a synthesis stream delivered no audio within
// its stall budget. It is deliberately distinct from context.DeadlineExceeded:
// the turn is still healthy and the engine is the thing that failed, so the
// pipeline can cover it with the fallback voice instead of abandoning the turn.
var ErrStreamStalled = errors.New("pcm stream stalled: no audio within budget")

// PCMStream carries synthesized mono PCM frames for playback.
type PCMStream struct {
	mu          sync.Mutex
	ctx         context.Context
	sampleRate  int
	err         error
	closed      bool
	incremental bool
	written     int
	ready       chan struct{}
	frames      chan []float32
	// stallBudget bounds the silence between frame batches, not the whole
	// utterance. A slow engine that keeps emitting audio keeps its segment;
	// one that emits nothing loses it. Zero leaves the stream unbounded.
	stallBudget time.Duration
}

// SetStallBudget bounds how long a consumer should wait for the next frame
// batch before treating the stream as stalled. Callers set it before handing
// the stream to a player. Zero (the default) disables the check.
func (s *PCMStream) SetStallBudget(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stallBudget = d
}

// StallBudget returns the per-batch budget set by SetStallBudget.
func (s *PCMStream) StallBudget() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stallBudget
}

// NewPCMStream creates a new PCM stream bound to ctx. A blocked Write unblocks
// and returns ctx.Err() when ctx is cancelled, so a cancelled turn never leaks
// the producing synth goroutine on the frames send.
func NewPCMStream(ctx context.Context) *PCMStream {
	if ctx == nil {
		ctx = context.Background()
	}
	return &PCMStream{
		ctx:    ctx,
		ready:  make(chan struct{}),
		frames: make(chan []float32, 8),
	}
}

// SetSampleRate sets the stream sample rate once before frames are written.
func (s *PCMStream) SetSampleRate(sampleRate int) error {
	if sampleRate <= 0 {
		return errors.New("sample rate must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sampleRate != 0 {
		if s.sampleRate != sampleRate {
			return errors.New("sample rate already set")
		}
		return nil
	}

	s.sampleRate = sampleRate
	close(s.ready)
	return nil
}

// SetIncremental declares that frames arrive *during* synthesis rather than
// all at once when it ends, so playback may start on the first frames instead
// of waiting for the whole utterance.
//
// It is a property of the producer, not a request from the consumer: only the
// synthesizer knows whether it is emitting PCM as it decodes (a Stage B Qwen
// worker) or handing over a finished blob (Kokoro, Stage A Qwen). A whole-blob
// producer must leave this false — starting playback from a partial buffer it
// filled in one burst gains nothing and risks draining mid-sentence.
//
// Call it before SetSampleRate, which is what the ordering rests on: a
// consumer only reads this after WaitReady has returned, and WaitReady cannot
// return until the rate is set. Setting it after that is a race the mutex
// hides — the flag would be correct and already too late to be read.
func (s *PCMStream) SetIncremental(incremental bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incremental = incremental
}

// Incremental reports whether this stream's frames arrive during synthesis.
func (s *PCMStream) Incremental() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.incremental
}

// WaitReady waits until the stream sample rate is known or the stream fails.
func (s *PCMStream) WaitReady(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.ready:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sampleRate == 0 {
			if s.err != nil {
				return 0, s.err
			}
			return 0, errors.New("pcm stream closed before sample rate was set")
		}
		return s.sampleRate, nil
	}
}

// Frames returns the stream's PCM frame channel.
func (s *PCMStream) Frames() <-chan []float32 {
	return s.frames
}

// Write pushes a frame batch into the stream.
func (s *PCMStream) Write(samples []float32) error {
	s.mu.Lock()
	if s.closed {
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return errors.New("pcm stream is closed")
	}
	if s.sampleRate == 0 {
		s.mu.Unlock()
		return errors.New("sample rate must be set before writing frames")
	}
	s.mu.Unlock()

	if len(samples) == 0 {
		return nil
	}

	chunk := make([]float32, len(samples))
	copy(chunk, samples)

	select {
	case s.frames <- chunk:
		s.mu.Lock()
		s.written += len(chunk)
		s.mu.Unlock()
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Written reports how many samples have been handed to the consumer. A
// producer that fails partway uses it to tell "nothing was heard yet, a retry
// is clean" from "some of this sentence has already played, and synthesizing
// it again would say those words twice".
func (s *PCMStream) Written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

// Close marks the stream complete.
func (s *PCMStream) Close() {
	s.CloseWithError(nil)
}

// CloseWithError marks the stream complete with an optional error.
func (s *PCMStream) CloseWithError(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.err = err
	if s.sampleRate == 0 {
		close(s.ready)
	}
	close(s.frames)
	s.mu.Unlock()
}

// Err returns the terminal stream error, if any.
func (s *PCMStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
