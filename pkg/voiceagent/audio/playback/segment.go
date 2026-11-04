package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"sync"
	"time"
)

type playbackSegment struct {
	attempt      audio.PlaybackAttempt
	mu           sync.Mutex
	audioSeconds float64
	samples      []int16
	offset       int
	readyFrames  int
	inputDone    bool
	err          error
	started      sync.Once
	done         sync.Once
	finishedCh   chan struct{}
	ready        chan struct{}
	startedCh    chan struct{}
	doneCh       chan audio.PlaybackResult
	readyClosed  bool
	interrupted  bool
	// audible latches once the device has taken frames from this segment.
	// Running dry before that is not an underrun — it is the segment waiting
	// its turn in the queue.
	audible bool
	// starved is true while the device is asking for audio this segment does
	// not have. It debounces the counter: one gap is one underrun, however
	// many callbacks it spans.
	starved   bool
	underruns int
	// inputEndedAt is when the synthesizer stopped producing for this segment.
	inputEndedAt time.Time
}

func newPlaybackSegment() *playbackSegment {
	return &playbackSegment{
		finishedCh: make(chan struct{}),
		ready:      make(chan struct{}),
		startedCh:  make(chan struct{}),
		doneCh:     make(chan audio.PlaybackResult, 1),
	}
}

func (s *playbackSegment) playback() *audio.Playback {
	return audio.NewPlayback(s.startedCh, s.doneCh)
}

func (s *playbackSegment) setReadyFrames(frames int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.readyFrames = frames
	s.maybeReadyLocked()
}

func (s *playbackSegment) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ready:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingLocked() > 0 {
		return nil
	}
	if s.interrupted {
		return context.Canceled
	}
	if s.err != nil {
		return s.err
	}
	return errors.New("pcm stream produced no samples")
}

// setAudioSeconds records how much audio the synthesizer produced, measured at
// the stream's own rate so a resample cannot change the figure.
func (s *playbackSegment) setAudioSeconds(samples []float32, inputRate int) {
	if inputRate <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioSeconds = float64(len(samples)) / float64(inputRate)
}

// addAudioSamples accumulates synthesized duration one chunk at a time. The
// whole-utterance path knows the total up front and calls setAudioSeconds; an
// incremental one only ever learns it a chunk at a time.
func (s *playbackSegment) addAudioSamples(count, inputRate int) {
	if count <= 0 || inputRate <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioSeconds += float64(count) / float64(inputRate)
}

// markInputEndedLocked stamps when the synthesizer stopped producing, once.
// Later terminal paths (a barge-in Stop arriving after the stream closed) must
// not overwrite it: the engine finished when it finished.
func (s *playbackSegment) markInputEndedLocked() {
	if s.inputEndedAt.IsZero() {
		s.inputEndedAt = time.Now()
	}
}

// resultLocked builds a terminal result carrying the synthesized duration.
// Every exit path goes through it so an interrupted or failed segment still
// reports what synthesis cost.
func (s *playbackSegment) resultLocked(err error, interrupted bool) audio.PlaybackResult {
	return s.attempt.Result(audio.PlaybackResult{
		Err:              err,
		Interrupted:      interrupted,
		AudioSeconds:     s.audioSeconds,
		Underruns:        s.underruns,
		SynthesisEndedAt: s.inputEndedAt,
	})
}

func (s *playbackSegment) append(samples []int16) {
	if len(samples) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inputDone || s.interrupted {
		return
	}

	if s.offset > 0 && s.offset == len(s.samples) {
		s.samples = s.samples[:0]
		s.offset = 0
	}

	s.samples = append(s.samples, samples...)
	s.maybeReadyLocked()
}

func (s *playbackSegment) finishInput(err error) {
	s.mu.Lock()
	if s.inputDone {
		s.mu.Unlock()
		return
	}
	s.inputDone = true
	s.err = err
	s.markInputEndedLocked()
	s.maybeReadyLocked()
	shouldComplete := s.pendingLocked() == 0
	result := s.resultLocked(s.err, s.interrupted)
	s.mu.Unlock()

	if shouldComplete {
		s.finish(result)
	}
}

func (s *playbackSegment) fail(err error) {
	s.mu.Lock()
	s.inputDone = true
	s.err = err
	s.markInputEndedLocked()
	s.maybeReadyLocked()
	result := s.resultLocked(err, false)
	s.mu.Unlock()

	s.finish(result)
}

func (s *playbackSegment) interrupt() {
	s.mu.Lock()
	s.inputDone = true
	s.interrupted = true
	s.samples = nil
	s.offset = 0
	s.maybeReadyLocked()
	result := s.resultLocked(nil, true)
	s.mu.Unlock()

	s.finish(result)
}

func (s *playbackSegment) complete() {
	s.mu.Lock()
	result := s.resultLocked(s.err, s.interrupted)
	s.mu.Unlock()

	s.finish(result)
}

func (s *playbackSegment) writeTo(output []byte, maxFrames int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.pendingLocked()
	if available == 0 {
		// Audible, still being synthesized, and out of audio: the listener is
		// about to hear a hole in the middle of a sentence. Count the gap, not
		// the callbacks it spans.
		if s.audible && !s.inputDone && !s.interrupted && !s.starved {
			s.starved = true
			s.underruns++
		}
		return 0, s.inputDone
	}
	s.starved = false

	if maxFrames > available {
		maxFrames = available
	}

	s.audible = true
	s.started.Do(func() { close(s.startedCh) })

	for i := range maxFrames {
		binary.LittleEndian.PutUint16(output[i*2:], uint16(s.samples[s.offset+i]))
	}
	s.offset += maxFrames

	if s.offset >= len(s.samples) {
		s.samples = s.samples[:0]
		s.offset = 0
	} else if s.offset >= playbackCompactionFrames && s.offset*2 >= len(s.samples) {
		copy(s.samples, s.samples[s.offset:])
		s.samples = s.samples[:len(s.samples)-s.offset]
		s.offset = 0
	}

	return maxFrames, s.pendingLocked() == 0 && s.inputDone
}

func (s *playbackSegment) pendingLocked() int {
	return len(s.samples) - s.offset
}

func (s *playbackSegment) maybeReadyLocked() {
	if s.readyClosed {
		return
	}

	pending := s.pendingLocked()
	switch {
	case pending > 0 && s.readyFrames > 0 && pending >= s.readyFrames:
		s.readyClosed = true
		close(s.ready)
	case s.inputDone:
		s.readyClosed = true
		close(s.ready)
	}
}

func (s *playbackSegment) finish(result audio.PlaybackResult) {
	if !s.attempt.Valid() {
		result.Interrupted = true
	}
	s.done.Do(func() {
		close(s.finishedCh)
		s.doneCh <- result
		close(s.doneCh)
	})
}
