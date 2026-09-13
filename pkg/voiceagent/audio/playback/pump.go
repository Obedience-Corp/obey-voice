package playback

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"time"
)

// runPlaybackWorker keeps an unexpected panic in device initialization or the
// stream pump from tearing down the TUI while Bubble Tea owns the terminal's
// raw and alternate-screen state. PlayStream owns stream, so the recovery path
// also drains it until the producer finishes or the preview is cancelled.
func runPlaybackWorker(ctx context.Context, segment *playbackSegment, stream *audio.PCMStream, pump func()) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		segment.fail(fmt.Errorf("audio playback worker panic: %v", recovered))
		for {
			select {
			case _, ok := <-stream.Frames():
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	pump()
}

func (p *Player) pumpSegment(ctx context.Context, segment *playbackSegment, stream *audio.PCMStream) {
	// A stalled engine has to surface as a segment failure rather than an
	// unbounded wait: the pipeline can only switch to the fallback voice if
	// this call returns. The budget measures the gap between frame batches,
	// not the whole utterance, so an engine that streams audio slowly keeps
	// its segment while one that produces nothing at all loses it.
	budget := stream.StallBudget()

	readyCtx := ctx
	if budget > 0 {
		var cancelReady context.CancelFunc
		readyCtx, cancelReady = context.WithTimeout(ctx, budget)
		defer cancelReady()
	}
	inputRate, err := stream.WaitReady(readyCtx)
	if err != nil {
		if budget > 0 && ctx.Err() == nil && errors.Is(readyCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("%w after %s", audio.ErrStreamStalled, budget)
		}
		segment.fail(err)
		return
	}

	outputRate, err := p.ensureDevice(inputRate)
	if err != nil {
		segment.fail(err)
		return
	}
	// ensureDevice only publishes the offset when it opens a device. Republish
	// here so the period the callback actually reported is picked up; the
	// front-end ignores an unchanged value.
	p.applyReferenceDelay()

	p.mu.Lock()
	debug := p.debug
	p.mu.Unlock()

	if stream.Incremental() {
		pumpIncremental(ctx, segment, stream, debug, inputRate, outputRate, budget)
		return
	}

	// Kokoro produces the complete sentence before exposing its PCM samples.
	// Buffer that already-generated sentence before handing it to the real-time
	// device callback. Starting from a partial buffer lets a brief scheduler
	// delay drain the segment and insert silence in the middle of speech.
	segment.setReadyFrames(0)
	var samples []float32

	// Reset on every batch: the budget bounds silence, not total duration.
	silence := newSilenceTimer(budget)
	defer silence.stop()

	for {
		select {
		case <-ctx.Done():
			segment.fail(ctx.Err())
			return
		case <-silence.C():
			segment.fail(fmt.Errorf("%w after %s", audio.ErrStreamStalled, budget))
			return
		case frames, ok := <-stream.Frames():
			if !ok {
				finalizeSegment(segment, debug, samples, inputRate, outputRate, stream.Err())
				return
			}
			if len(frames) == 0 {
				continue
			}
			samples = append(samples, frames...)
			silence.reset()
		}
	}
}

// silenceTimer fires when no reset arrives within budget. A zero or negative
// budget yields a timer that never fires, so an unbounded stream keeps the
// pre-existing behaviour of waiting as long as the turn allows.
//
// It exists so an engine that delivers audio slowly is judged on whether audio
// is still arriving rather than on total synthesis time: every batch resets it.
type silenceTimer struct {
	budget time.Duration
	timer  *time.Timer
}

func newSilenceTimer(budget time.Duration) *silenceTimer {
	s := &silenceTimer{budget: budget}
	if budget > 0 {
		s.timer = time.NewTimer(budget)
	}
	return s
}

// C is the fire channel, nil when the timer is disabled so a select on it
// blocks forever.
func (s *silenceTimer) C() <-chan time.Time {
	if s.timer == nil {
		return nil
	}
	return s.timer.C
}

// reset restarts the budget, draining an already-delivered tick so a batch that
// races the deadline does not leave a stale fire queued.
func (s *silenceTimer) reset() {
	if s.timer == nil {
		return
	}
	if !s.timer.Stop() {
		select {
		case <-s.timer.C:
		default:
		}
	}
	s.timer.Reset(s.budget)
}

func (s *silenceTimer) stop() {
	if s.timer != nil {
		s.timer.Stop()
	}
}

// pumpIncremental releases audio to the device as it arrives instead of
// buffering the utterance first. It is the whole point of a streaming
// synthesizer: the first chunk becomes audible while the rest is still being
// generated, so time-to-first-audio stops being the full synthesis.
//
// Resampling is the reason this cannot just call finalizeSegment per chunk.
// resampleLinear is a whole-buffer operation that restarts its phase at zero
// and clamps its own tail, so running it once per chunk would drop and repeat
// samples at every chunk boundary — a click per chunk. streamResampler carries
// the fractional phase and the interpolation overlap across calls, which is
// what makes a chunked stream reconstruct as one continuous signal.
//
// The stall budget is enforced exactly as the buffered path enforces it, and
// for the same reason: it bounds silence between batches, never the utterance.
// A worker that is slow but still emitting chunks resets the timer on every one
// of them and keeps its segment — which is precisely the engine this path
// exists for.
func pumpIncremental(ctx context.Context, segment *playbackSegment, stream *audio.PCMStream, debug *playerDebugRecorder, inputRate, outputRate int, budget time.Duration) {
	segment.setReadyFrames(preRollFrames(outputRate))

	var resampler streamResampler
	resampler.Configure(inputRate, outputRate)
	var converted []float32

	silence := newSilenceTimer(budget)
	defer silence.stop()

	// The debug recorder writes one WAV per captured source, so the chunks are
	// rejoined and handed over once, on every exit path. Capturing per chunk
	// would scatter a single sentence across dozens of files and make the
	// capture useless for hearing the joins — the thing it would be opened to
	// check. A partial utterance is still worth writing: a segment that
	// stalled or was cancelled is exactly when someone goes looking.
	var captured []float32
	if debug != nil {
		defer func() { debug.captureSource(inputRate, captured) }()
	}

	for {
		select {
		case <-ctx.Done():
			segment.fail(ctx.Err())
			return
		case <-silence.C():
			segment.fail(fmt.Errorf("%w after %s", audio.ErrStreamStalled, budget))
			return
		case frames, ok := <-stream.Frames():
			if !ok {
				segment.finishInput(stream.Err())
				return
			}
			if len(frames) == 0 {
				continue
			}
			if debug != nil {
				captured = append(captured, frames...)
			}
			// Duration is measured at the input rate and accumulated per
			// chunk: nothing here ever sees the whole utterance at once.
			segment.addAudioSamples(len(frames), inputRate)
			if inputRate == outputRate {
				segment.append(float32ToPCM16(frames))
			} else {
				converted = resampleChunk(&resampler, frames, converted, inputRate, outputRate)
				segment.append(float32ToPCM16(converted))
			}
			silence.reset()
		}
	}
}

// preRollFrames converts incrementalPreRoll into output frames, flooring at one
// device period so a pathological rate cannot make a segment audible with
// nothing behind it.
func preRollFrames(outputRate int) int {
	if outputRate <= 0 {
		return playbackPeriodFrames
	}
	frames := int(float64(outputRate) * incrementalPreRoll.Seconds())
	if frames < playbackPeriodFrames {
		return playbackPeriodFrames
	}
	return frames
}

// resampleChunk converts one chunk through the phase-preserving resampler,
// reusing buf. Whatever cannot be interpolated yet (at most one sample, needed
// as the left neighbour of the next chunk's first output) stays in the
// resampler's carry rather than being flushed with a duplicated edge.
func resampleChunk(resampler *streamResampler, frames, buf []float32, inputRate, outputRate int) []float32 {
	need := len(frames)*outputRate/inputRate + 2
	if cap(buf) < need {
		buf = make([]float32, need)
	}
	buf = buf[:need]
	return buf[:resampler.Resample(frames, buf)]
}

// finalizeSegment resamples the fully-buffered utterance once and appends it
// to segment regardless of streamErr: a stream that fails partway through
// (e.g. a cancelled turn) may already have produced audio worth playing, and
// the caller still learns about the failure through the segment's terminal
// PlaybackResult once that audio finishes.
//
// AEC reference is NOT pushed here. Bulk-pushing the whole utterance before the
// device callback runs lets ProcessCapture drain the ref queue while the segment
// is still waiting to play — so by the time speakers emit, the canceller has
// nothing left and barge-in hears her own voice. onData feeds the reference
// sample-clock-aligned as audio actually leaves the device (via aecIngress).
func finalizeSegment(segment *playbackSegment, debug *playerDebugRecorder, samples []float32, inputRate, outputRate int, streamErr error) {
	if debug != nil {
		debug.captureSource(inputRate, samples)
	}
	segment.setAudioSeconds(samples, inputRate)
	if inputRate != outputRate {
		samples = resample(samples, inputRate, outputRate)
	}
	segment.append(float32ToPCM16(samples))
	segment.finishInput(streamErr)
}
