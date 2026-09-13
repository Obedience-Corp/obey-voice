package stt

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// levelEmitMinInterval caps UI meter events so high-rate capture never floods
// the session channel or the TUI bridge.
const levelEmitMinInterval = 50 * time.Millisecond

// speechRMSScale maps typical close-mic speech RMS (~0.02–0.15) into 0..1.
const speechRMSScale = 0.12

// minSpeechRMS is the lowest RMS that counts as a talker, measured at the
// microphone. Digital silence and STT-on-zeros is ~0; below this we did not
// hear anyone — Silero still false-marks, and the decoder invents "yeah".
//
// This is a not-silence floor, not a close-mic assumption. Desk speech is
// ~0.02–0.15; a talker across a table on a laptop mic is often ~0.002–0.006.
// 0.008 dropped those room voices. Stationary fans still fail the noise gate.
//
// It is a *pre-gain* number: apply it with speechFloor, never directly to
// samples a front-end has already amplified.
const minSpeechRMS = 0.002

// speechFloor scales minSpeechRMS by the gain already applied to the samples
// being measured. AGC lifts room tone and talkers by the same factor, so a
// fixed post-gain floor has to choose between admitting noise and rejecting
// the far talkers it exists to hear: meeting capture's 8× ListenGain reads
// 0.001 room tone as 0.008 and a 0.002 far talker as 0.016. Scaling keeps the
// question in microphone units, so the answer is the same with or without a
// front-end. Gain below 1 (a close talker the AGC is cutting) never raises the
// floor.
func speechFloor(gain float64) float64 {
	if gain <= 1 || math.IsNaN(gain) {
		return minSpeechRMS
	}
	return minSpeechRMS * gain
}

// inputGain reports the gain a frame source has already applied to the frames
// it hands out. Sources that do not amplify (files, fakes, a capture with no
// front-end) report 1.
func inputGain(frames audio.FrameSource) float64 {
	if g, ok := frames.(audio.GainReporter); ok {
		return g.InputGain()
	}
	return 1
}

// sendEvent delivers an event, preferring a non-blocking send and falling back
// to a cancellable blocking send when the channel is full.
func sendEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	default:
	}
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

// trySendEvent never blocks. Used for droppable meter samples.
func trySendEvent(events chan<- Event, event Event) {
	select {
	case events <- event:
	default:
	}
}

// audibleWindow is 100ms at capture rate: the shortest stretch judged for a
// talker, and the only audio a stream has to keep to judge the next one.
const audibleWindow = 1600

// audibleSpeech reports whether samples contain a talker, not just enough
// length for the decoder to run. Pre-roll silence can dilute whole-utterance
// RMS, so any 100ms window above the floor counts. Samples must be unamplified;
// use audibleSpeechAbove with speechFloor for a gained path.
func audibleSpeech(samples []float32) bool {
	return audibleSpeechAbove(samples, minSpeechRMS)
}

// audibleSpeechAbove is audibleSpeech against an explicit floor.
func audibleSpeechAbove(samples []float32, floor float64) bool {
	t := audibleTracker{floor: floor}
	t.push(samples)
	return t.audible()
}

// audibleTracker answers audibleSpeech's question over a stream that arrives
// frame by frame, in constant space. A live capture runs for as long as the
// user leaves it open, so the audio cannot be retained to be re-measured at the
// endpoint: once a window clears the floor the verdict latches, and only the
// window being filled plus the trailing 100ms are kept.
type audibleTracker struct {
	floor float64                // not-silence floor; 0 uses minSpeechRMS
	heard bool                   // a completed window already cleared the floor
	sumSq float64                // energy of the window being filled
	n     int                    // samples in the window being filled
	tail  [audibleWindow]float32 // most recent 100ms, for the trailing window
	i     int                    // next write index into tail
	fill  int                    // samples held in tail
}

// speechFloor is the floor this tracker judges against.
func (t *audibleTracker) speechFloor() float64 {
	if t.floor > 0 {
		return t.floor
	}
	return minSpeechRMS
}

// push folds one frame into the verdict and discards the audio behind it.
func (t *audibleTracker) push(samples []float32) {
	if t.heard {
		return
	}
	for _, s := range samples {
		v := float64(s)
		t.sumSq += v * v
		t.n++
		t.tail[t.i] = s
		t.i = (t.i + 1) % audibleWindow
		if t.fill < audibleWindow {
			t.fill++
		}
		if t.n < audibleWindow {
			continue
		}
		if math.Sqrt(t.sumSq/float64(audibleWindow)) >= t.speechFloor() {
			t.heard = true
			return
		}
		t.sumSq = 0
		t.n = 0
	}
}

// audible reports the verdict for everything pushed so far. The trailing window
// is measured here rather than on the way in, so a word straddling a window
// boundary at the end of the stream still counts. RMS ignores sample order, so
// the ring needs no unwrapping.
func (t *audibleTracker) audible() bool {
	if t.heard {
		return true
	}
	if t.fill == 0 {
		return false
	}
	return frameRMS(t.tail[:t.fill]) >= t.speechFloor()
}

// reset drops the verdict and the retained window for a fresh utterance. The
// floor belongs to the path, not the utterance, so it survives.
func (t *audibleTracker) reset() { *t = audibleTracker{floor: t.floor} }

// frameRMS returns the root-mean-square amplitude of mono samples.
func frameRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// normalizeInputLevel maps raw RMS into a 0..1 UI meter reading.
func normalizeInputLevel(rms float64) float64 {
	if rms <= 0 {
		return 0
	}
	level := rms / speechRMSScale
	if level > 1 {
		return 1
	}
	return level
}

// levelEmitter throttles InputLevel samples so the TUI can animate without
// back-pressuring capture.
type levelEmitter struct {
	last time.Time
}

// maybeEmit sends a non-blocking InputLevel when the throttle window has elapsed.
func (e *levelEmitter) maybeEmit(events chan<- Event, samples []float32) {
	if len(samples) == 0 {
		return
	}
	now := time.Now()
	if !e.last.IsZero() && now.Sub(e.last) < levelEmitMinInterval {
		return
	}
	e.last = now
	trySendEvent(events, InputLevel{Level: normalizeInputLevel(frameRMS(samples))})
}

// newPauseAwarePhaseEmitter stamps the elapsed time
// between phases. It returns false when the context is cancelled mid-send, so
// loops can stop promptly. A yielded owner must keep processing capture even
// when its consumer has stopped draining advisory phase events.
func newPauseAwarePhaseEmitter(ctx context.Context, events chan<- Event, pause *pauseController) func(string) bool {
	lastPhaseAt := time.Now()
	return func(phase string) bool {
		now := time.Now()
		if !sendPauseAwareAdvisoryEvent(ctx, pause, events, PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}) {
			return false
		}
		lastPhaseAt = now
		return true
	}
}

// sendPauseAwareAdvisoryEvent keeps capture arbitration live when the STT
// consumer has intentionally stopped draining its advisory event stream.
func sendPauseAwareAdvisoryEvent(ctx context.Context, pause *pauseController, events chan<- Event, event Event) bool {
	if pause != nil && pause.advisoryBackpressured() {
		trySendEvent(events, event)
		return true
	}
	return sendEvent(ctx, events, event)
}

// frameRead is the triaged result of one FrameSource read, shared by the
// provider loops so both classify source behavior identically.
type frameRead struct {
	frame audio.Frame
	ready bool  // a frame (possibly Final) is available
	eof   bool  // the source has ended
	err   error // fatal: the loop should emit Failure and return
}

// readLoopFrame reads one frame and classifies the result. ErrNoFrameReady is
// not an error: it yields an empty read so the loop can still evaluate the
// endpoint policy before polling again.
func readLoopFrame(ctx context.Context, frames audio.FrameSource) frameRead {
	frame, err := frames.ReadFrame(ctx)
	switch {
	case err == nil:
		return frameRead{frame: frame, ready: true, eof: frame.Final}
	case errors.Is(err, audio.ErrNoFrameReady):
		return frameRead{}
	case errors.Is(err, audio.ErrSourceClosed):
		return frameRead{frame: audio.Frame{Final: true}, ready: true, eof: true}
	default:
		return frameRead{err: err}
	}
}

// frameDur returns the wall-time a frame represents, preferring the source's
// own stamp and falling back to the sample count for sources that omit it.
func frameDur(f audio.Frame) time.Duration {
	if f.Duration > 0 {
		return f.Duration
	}
	return audio.SamplesDuration(len(f.Samples))
}

// speechTracker accumulates the speech facts the endpoint policy evaluates.
// It anchors the utterance clock at speech onset so pre-speech silence never
// deducts from the allowed utterance length.
type speechTracker struct {
	listenStart time.Time
	speechStart time.Time
	detected    bool
	seen        time.Duration
	silence     time.Duration
}

func newSpeechTracker() *speechTracker {
	return &speechTracker{listenStart: time.Now()}
}

// observe folds one frame's VAD verdict into the tracker, reporting speech onset.
func (t *speechTracker) observe(isSpeech bool, dur time.Duration) (onset bool) {
	if isSpeech {
		onset = t.markSpeech()
		t.seen += dur
		t.silence = 0
		return onset
	}
	if t.detected {
		t.silence += dur
	}
	return false
}

// markSpeech records speech onset from a non-VAD signal (a recognizer partial,
// an EOF flush), reporting whether this call was the onset.
func (t *speechTracker) markSpeech() bool {
	if t.detected {
		return false
	}
	t.detected = true
	t.speechStart = time.Now()
	return true
}

// observation snapshots the tracker for the policy. It is evaluated once per
// loop iteration — including no-frame ticks — so every endpoint decision is
// enforced even when the source stops delivering frames mid-utterance.
func (t *speechTracker) observation(providerEnd, eof bool) endpoint.Observation {
	o := endpoint.Observation{
		HasSpeech:       t.detected,
		SpeechSeen:      t.seen,
		TrailingSilence: t.silence,
		Elapsed:         time.Since(t.listenStart),
		ProviderEnd:     providerEnd,
		SourceFinal:     eof,
	}
	if t.detected {
		o.SpeechElapsed = time.Since(t.speechStart)
	}
	return o
}

// reset returns the tracker to a fresh listening window.
func (t *speechTracker) reset() {
	t.detected = false
	t.seen = 0
	t.silence = 0
	t.listenStart = time.Now()
	t.speechStart = time.Time{}
}
