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

// streamProperties are the facts that describe how a stream must be *consumed*,
// as opposed to what it currently holds. Every one of them is set by the
// producer before any frame is written and read by the player once, so any
// component that re-frames a stream — a tee, a fanout, a resampling adapter —
// has to carry all of them or it silently changes how the audio is played.
//
// They are grouped in one struct so Derive can copy them in a single
// assignment. That is the point: the fanout used to copy the sample rate by
// hand, which meant every property added afterwards was dropped without a
// compile error and without a failing test. A new field added here is carried
// by every existing caller of Derive for free, and TestPCMStreamPropertiesAreDerived
// fails if one is added outside this struct instead.
type streamProperties struct {
	sampleRate  int
	incremental bool
	// stallBudget bounds the silence between frame batches, not the whole
	// utterance. A slow engine that keeps emitting audio keeps its segment;
	// one that emits nothing loses it. Zero leaves the stream unbounded.
	stallBudget time.Duration
}

// PCMStream carries synthesized mono PCM frames for playback.
type PCMStream struct {
	mu    sync.Mutex
	ctx   context.Context
	props streamProperties
	// The rest is this stream's own lifecycle and payload — never derived,
	// because a derived stream carries its own.
	err                error
	closed             bool
	written            int
	firstWrittenAt     time.Time
	closedAt           time.Time
	synthesisWarmState string
	ready              chan struct{}
	frames             chan []float32
}

// SetStallBudget bounds how long a consumer should wait for the next frame
// batch before treating the stream as stalled. Callers set it before handing
// the stream to a player. Zero (the default) disables the check.
func (s *PCMStream) SetStallBudget(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.props.stallBudget = d
}

// StallBudget returns the per-batch budget set by SetStallBudget.
func (s *PCMStream) StallBudget() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.props.stallBudget
}

// Derive returns a new, empty stream that carries every property of s and is
// bound to ctx. Anything that tees or re-frames a stream must build the copy
// this way rather than with NewPCMStream, so that how the audio is meant to be
// played travels with it.
//
// It copies properties only. The error, the closed flag, the frames already
// written and the frame channel all belong to the stream that owns them: a
// derived stream is a fresh pipe with the same instructions on it.
//
// When s has no sample rate yet the derived stream has none either, and the
// caller must set it before writing — the same contract NewPCMStream has.
func (s *PCMStream) Derive(ctx context.Context) *PCMStream {
	s.mu.Lock()
	props := s.props
	s.mu.Unlock()

	derived := NewPCMStream(ctx)
	derived.props = props
	if props.sampleRate > 0 {
		// SetSampleRate's side effect, without its validation: the value came
		// from a stream that already passed it.
		close(derived.ready)
	}
	return derived
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

	if s.props.sampleRate != 0 {
		if s.props.sampleRate != sampleRate {
			return errors.New("sample rate already set")
		}
		return nil
	}

	s.props.sampleRate = sampleRate
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
	s.props.incremental = incremental
}

// Incremental reports whether this stream's frames arrive during synthesis.
func (s *PCMStream) Incremental() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.props.incremental
}

// WaitReady waits until the stream sample rate is known or the stream fails.
func (s *PCMStream) WaitReady(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.ready:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.props.sampleRate == 0 {
			if s.err != nil {
				return 0, s.err
			}
			return 0, errors.New("pcm stream closed before sample rate was set")
		}
		return s.props.sampleRate, nil
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
	if s.props.sampleRate == 0 {
		s.mu.Unlock()
		return errors.New("sample rate must be set before writing frames")
	}
	if len(samples) == 0 {
		s.mu.Unlock()
		return nil
	}

	chunk := make([]float32, len(samples))
	copy(chunk, samples)
	if s.firstWrittenAt.IsZero() {
		// The first send is made while holding mu so a consumer that receives
		// this frame cannot observe it before its publication timestamp. A new
		// stream has an empty buffered channel, so this first send cannot block;
		// later writes release mu before a potentially blocking send.
		select {
		case s.frames <- chunk:
			s.written += len(chunk)
			s.firstWrittenAt = time.Now()
			s.mu.Unlock()
			return nil
		case <-s.ctx.Done():
			s.mu.Unlock()
			return s.ctx.Err()
		}
	}
	s.mu.Unlock()

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

// FirstWrittenAt reports when the first non-empty PCM batch was handed to the
// stream consumer. A zero value means no PCM has been observed.
func (s *PCMStream) FirstWrittenAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstWrittenAt
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
	s.closedAt = time.Now()
	if s.props.sampleRate == 0 {
		close(s.ready)
	}
	close(s.frames)
	s.mu.Unlock()
}

// ClosedAt reports when the producer closed the stream. It is an observed
// synthesis boundary; callers must leave it unknown when the stream is not
// available rather than deriving it from a requested duration.
func (s *PCMStream) ClosedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closedAt
}

// SetSynthesisWarmState records the provider's request-specific warmness
// evidence. It is lifecycle metadata owned by this stream and is not copied
// by Derive. Empty means the provider did not publish request evidence.
func (s *PCMStream) SetSynthesisWarmState(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synthesisWarmState = state
}

// SynthesisWarmState reports request-specific warmness evidence, or empty when
// the provider did not publish it.
func (s *PCMStream) SynthesisWarmState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.synthesisWarmState
}

// Err returns the terminal stream error, if any.
func (s *PCMStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
