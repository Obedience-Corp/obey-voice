package main

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

var _ audio.Engine = (*recordingPlayer)(nil)

// recordingPlayer implements audio.Engine by draining PCM and writing WAVs.
// IsPlaying is true only while a drain is in flight (or a --speakers tee is
// still playing), never for the whole turn.
type recordingPlayer struct {
	mu         sync.Mutex
	dir        string
	samples    []float32
	rate       int
	real       audio.Engine
	closed     bool
	inflight   atomic.Int32
	stopped    atomic.Bool
	cancels    map[int]context.CancelFunc
	nextID     int
	generation uint64
	scored     []wavAnalysis
}

func newRecordingPlayer(real audio.Engine) *recordingPlayer {
	return &recordingPlayer{
		real:    real,
		cancels: make(map[int]context.CancelFunc),
	}
}

// SinkKind distinguishes a WAV recording from physical playback. A recording
// alone must never satisfy an audible result-delivery receipt.
func (p *recordingPlayer) SinkKind() string {
	if p.real == nil {
		return audio.SinkNone
	}
	if reporter, ok := p.real.(audio.SinkReporter); ok {
		return reporter.SinkKind()
	}
	return audio.SinkLocalSpeaker
}

func (p *recordingPlayer) BeginTurn(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
	p.dir = dir
	p.samples = p.samples[:0]
	p.rate = 0
	return nil
}

func (p *recordingPlayer) EndTurn() error {
	p.mu.Lock()
	dir := p.dir
	samples := append([]float32(nil), p.samples...)
	rate := p.rate
	p.mu.Unlock()
	if dir == "" {
		return nil
	}
	if rate <= 0 {
		rate = audio.SampleRate
	}
	wavPath := filepath.Join(dir, "playback.wav")
	// ReadWAVFloat32 treats a zero-length data chunk as missing, so skip the
	// file when the turn produced no PCM (wait-only drain, self-test).
	if len(samples) > 0 {
		if err := audio.WriteWAVFloat32(wavPath, rate, samples); err != nil {
			return err
		}
	}
	a := scoreSamples(wavPath, samples, rate)
	if err := writeAnalysis(filepath.Join(dir, "analysis.json"), a); err != nil {
		return err
	}
	p.mu.Lock()
	p.scored = append(p.scored, a)
	p.mu.Unlock()
	return nil
}

func (p *recordingPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if stream == nil {
		return failedPlayback(errors.New("nil pcm stream")), nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		go drainDiscard(stream)
		return failedPlayback(errors.New("recording player is closed")), nil
	}
	// Register cancellation before launching the drain: Stop must see even a
	// stream whose goroutine has not begun running yet.
	drainCtx, cancel := context.WithCancel(ctx)
	if p.cancels == nil {
		p.cancels = make(map[int]context.CancelFunc)
	}
	p.nextID++
	id := p.nextID
	p.cancels[id] = cancel
	generation := p.generation
	p.stopped.Store(false)
	p.inflight.Add(1)
	p.mu.Unlock()

	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go p.play(drainCtx, cancel, id, generation, stream, started, done)
	return audio.NewPlayback(started, done), nil
}

func (p *recordingPlayer) play(drainCtx context.Context, cancel context.CancelFunc, id int, generation uint64, stream *audio.PCMStream, started chan struct{}, done chan audio.PlaybackResult) {
	var result audio.PlaybackResult
	defer func() {
		cancel()
		p.removeCancel(id)
		p.inflight.Add(-1)
		done <- audio.PlaybackAttemptFromContext(drainCtx).Result(result)
		close(done)
	}()

	rate, err := stream.WaitReady(drainCtx)
	if err != nil {
		drainDiscard(stream)
		close(started)
		result = audio.PlaybackResult{Err: err}
		return
	}
	p.setRate(rate, generation)
	derived, teeReady := p.startTee(drainCtx, stream, started)
	interrupted, playErr := p.recordFrames(drainCtx, stream, derived, rate, generation)
	if derived != nil {
		derived.Close()
	}
	if teeReady != nil {
		select {
		case tee := <-teeReady:
			playErr = errors.Join(playErr, tee.err)
			if tee.playback != nil {
				select {
				case result = <-tee.playback.Done():
				case <-drainCtx.Done():
					result = audio.PlaybackResult{Interrupted: true, Err: drainCtx.Err()}
				}
			}
		case <-drainCtx.Done():
			playErr = errors.Join(playErr, drainCtx.Err())
		}
	}
	result.Err = errors.Join(result.Err, playErr)
	result.Interrupted = result.Interrupted || interrupted
}

type recordingTeeStart struct {
	playback *audio.Playback
	err      error
}

// A real player can wait for its initial PCM buffer before PlayStream returns.
// Start it concurrently with recording so that buffer can actually be filled.
func (p *recordingPlayer) startTee(ctx context.Context, stream *audio.PCMStream, started chan struct{}) (*audio.PCMStream, <-chan recordingTeeStart) {
	if p.real == nil {
		close(started)
		return nil, nil
	}
	derived := stream.Derive(ctx)
	ready := make(chan recordingTeeStart, 1)
	go func() {
		defer close(started)
		pb, err := p.real.PlayStream(ctx, derived)
		if err == nil && pb == nil {
			err = errors.New("speaker returned no playback handle")
		}
		if err != nil {
			go drainDiscard(derived)
		}
		ready <- recordingTeeStart{playback: pb, err: err}
		if pb != nil {
			select {
			case <-pb.Started():
			case <-ctx.Done():
			}
		}
	}()
	return derived, ready
}

func (p *recordingPlayer) recordFrames(ctx context.Context, stream, derived *audio.PCMStream, rate int, generation uint64) (bool, error) {
	frames := stream.Frames()
	for {
		select {
		case <-ctx.Done():
			for range frames {
			}
			return true, ctx.Err()
		case frame, ok := <-frames:
			if !ok {
				return false, nil
			}
			p.append(frame, rate, generation)
			if derived != nil {
				_ = derived.Write(frame)
			}
		}
	}
}

func (p *recordingPlayer) Stop() {
	p.stopped.Store(true)
	p.mu.Lock()
	p.generation++
	cs := p.cancels
	p.cancels = make(map[int]context.CancelFunc)
	real := p.real
	p.mu.Unlock()
	for _, c := range cs {
		c()
	}
	if real != nil {
		real.Stop()
	}
}

func (p *recordingPlayer) IsPlaying() bool {
	if p.stopped.Load() {
		return false
	}
	return p.inflight.Load() > 0
}

func (p *recordingPlayer) Close() error {
	p.mu.Lock()
	p.closed = true
	real := p.real
	p.mu.Unlock()
	p.Stop()
	if real != nil {
		return real.Close()
	}
	return nil
}

func (p *recordingPlayer) analyses() []wavAnalysis {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wavAnalysis, len(p.scored))
	copy(out, p.scored)
	return out
}

func (p *recordingPlayer) failingCount() int {
	n := 0
	for _, a := range p.analyses() {
		if a.HasCrackle {
			n++
		}
	}
	return n
}

func (p *recordingPlayer) scoredCount() int {
	n := 0
	for _, a := range p.analyses() {
		if a.SampleCount > 0 {
			n++
		}
	}
	return n
}

func (p *recordingPlayer) removeCancel(id int) {
	p.mu.Lock()
	delete(p.cancels, id)
	p.mu.Unlock()
}

func (p *recordingPlayer) activeCancels() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cancels)
}

func (p *recordingPlayer) setRate(rate int, generation uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return
	}
	if p.rate == 0 && rate > 0 {
		p.rate = rate
	}
}

func (p *recordingPlayer) append(frame []float32, rate int, generation uint64) {
	if len(frame) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return
	}
	if p.rate == 0 && rate > 0 {
		p.rate = rate
	}
	if p.rate > 0 && rate > 0 && rate != p.rate {
		frame = resampleLinear(frame, rate, p.rate)
	}
	p.samples = append(p.samples, frame...)
}

func failedPlayback(err error) *audio.Playback {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	close(started)
	done <- audio.PlaybackResult{Err: err}
	close(done)
	return audio.NewPlayback(started, done)
}

func drainDiscard(stream *audio.PCMStream) {
	if stream == nil {
		return
	}
	for range stream.Frames() {
	}
}

func resampleLinear(samples []float32, from, to int) []float32 {
	if len(samples) == 0 || from <= 0 || to <= 0 || from == to {
		return samples
	}
	outLen := int(math.Round(float64(len(samples)) * float64(to) / float64(from)))
	if outLen < 1 {
		outLen = 1
	}
	out := make([]float32, outLen)
	step := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * step
		index := int(pos)
		if index >= len(samples)-1 {
			out[i] = samples[len(samples)-1]
			continue
		}
		frac := float32(pos - float64(index))
		out[i] = samples[index] + (samples[index+1]-samples[index])*frac
	}
	return out
}
