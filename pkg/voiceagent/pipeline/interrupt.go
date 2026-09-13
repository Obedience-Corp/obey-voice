package pipeline

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

const (
	// bargeInArmDelay holds off interrupt detection after the turn's first
	// playback starts so the echo of Obey Voice's own first words doesn't trip
	// barge-in. Keep this short enough that "stop" / "wait" still interrupt
	// promptly once armed; AEC reference timing (not a long speech threshold)
	// is what suppresses self-echo.
	//
	// The delay applies once per turn (see applyPlaybackEvent), not once per
	// sentence: re-arming on every playback start left sentences shorter than
	// the window uninterruptible and kept barge-in permanently disarmed across
	// back-to-back short sentences (WI-dc9e33 B3 / F3.2).
	bargeInArmDelay = 600 * time.Millisecond
	// bargeInMinSpeechChunks requires sustained speech before interrupting, so a
	// brief burst of residual echo isn't mistaken for the user. One chunk is one
	// capture callback, whose negotiated size varies by device (for example,
	// 10ms or 60ms). Neural speech must also pass STT's audibility check; quiet
	// residual echo can sustain neural votes without containing audible speech.
	bargeInMinSpeechChunks = 6
	// bargeInBufferSize is the capture subscription depth for the controller.
	bargeInBufferSize = 8
	// bargeInPauseTailGuardDefault skips capture chunks for a short window
	// after playback stops, when Pipeline.BargeInTailGuard is unset (zero).
	// Speaker echo keeps hitting the mic briefly after the player drains
	// between sentences; now that pauses stay armed, that tail must not
	// accumulate toward a trip. Chunks inside the guard are skipped, not
	// cleared — an utterance spanning the gap keeps its speech streak.
	// Config-driven via barge_in_tail_guard_ms (200-800ms); see
	// Pipeline.bargeInTailGuard.
	bargeInPauseTailGuardDefault = 200 * time.Millisecond
	// bargeInNearMissWindow bounds how recent below-threshold pause speech must
	// be for the turn to keep the capture buffer for the next listen (F3.3):
	// the user spoke during a pause but the turn completed before the trip
	// threshold, so their utterance is sitting in the capture ring buffer and
	// Reset() would delete it right before STT opens.
	bargeInNearMissWindow = 1500 * time.Millisecond
	// bargeInSpeakerVerdictMaxAgeDefault is the fallback staleness bound when
	// Pipeline.BargeInSpeakerVerdictMaxAge is unset (zero): 2x
	// speaker.live.window_ms's own default (1500ms). See
	// Pipeline.bargeInSpeakerVerdictMaxAge and config.BargeInSpeakerVerdictMaxAgeOf,
	// which computes the configured value from the real window.
	bargeInSpeakerVerdictMaxAgeDefault = 3 * time.Second
)

// SpeakerVerdict is the interrupt controller's narrow view of the live
// speaker analyzer's most recent window result (S3, D7). It mirrors the
// fields ownerVerifyGate already checks on internal/speaker.LiveStats
// (Status/LastLabel/LastEnrollRev) rather than importing that package, the
// same decoupling SpeakerGate and CurrentSpeaker already use.
type SpeakerVerdict struct {
	// Healthy reports the analyzer is confidently running — mirrors
	// ownerVerifyGate's LiveHealthy/LiveRunning check.
	Healthy bool
	// HasVerdict reports at least one window has been analyzed. False during
	// the cold-start window after arming (~1.5s) before LiveFeed's first
	// window lands.
	HasVerdict bool
	// Enrolled reports the most recent window matched an enrolled profile.
	Enrolled bool
	// Label is the most recent speaker label. Diagnostics only; the trip
	// decision uses Enrolled, not the label itself.
	Label string
	// Confidence is the most recent match confidence. Diagnostics only.
	Confidence float32
	// At is when this verdict was produced, used for the staleness check
	// (Pipeline.bargeInSpeakerVerdictMaxAge). A zero value is always stale.
	At time.Time
}

// interruptRequest is the typed barge-in signal the interrupt controller reports
// to the turn runtime, which owns the decision to act on it.
type interruptRequest struct {
	Reason string
	At     time.Time
}

// playbackState is the narrow view of playback the interrupt controller needs —
// only whether audio is currently playing — keeping it decoupled from playback
// internals.
type playbackState interface {
	IsPlaying() bool
}

// interruptController watches the capture stream for sustained user speech
// during playback and reports a single barge-in request. It owns its capture
// subscription and VAD lifecycle; its thresholds are explicit fields so tests
// can drive the arm delay and speech threshold deterministically.
type interruptController struct {
	capture    CaptureMonitor
	vad        VoiceDetector
	playback   playbackState
	armDelay   time.Duration
	tailGuard  time.Duration
	minSpeech  int
	bufferSize int

	// speakerVerdict is nil for barge_in_speakers=anyone (never consulted).
	// Set, it gates a trip through allowsInterrupt (D7).
	speakerVerdict func() SpeakerVerdict
	verdictMaxAge  time.Duration

	// lastPauseSpeech is the UnixNano of the newest speech chunk heard while
	// armed but not playing — the near-miss evidence sawPauseSpeechWithin
	// reports after the watcher goroutine has been joined.
	lastPauseSpeech atomic.Int64
}

type interruptWatch struct {
	requests <-chan interruptRequest
	done     <-chan struct{}
}

// newInterruptController builds the controller from the pipeline's collaborators
// and the package barge-in tuning constants.
func (p *Pipeline) newInterruptController() *interruptController {
	return &interruptController{
		capture:        p.Capture,
		vad:            p.BargeInVAD,
		playback:       p.Player,
		armDelay:       bargeInArmDelay,
		tailGuard:      p.bargeInTailGuard(),
		minSpeech:      bargeInMinSpeechChunks,
		bufferSize:     bargeInBufferSize,
		speakerVerdict: p.BargeInSpeakerVerdict,
		verdictMaxAge:  p.bargeInSpeakerVerdictMaxAge(),
	}
}

// bargeInSpeakerVerdictMaxAge returns Pipeline.BargeInSpeakerVerdictMaxAge
// when set (positive), otherwise the built-in default. Mirrors
// bargeInTailGuard's override pattern.
func (p *Pipeline) bargeInSpeakerVerdictMaxAge() time.Duration {
	if p.BargeInSpeakerVerdictMaxAge > 0 {
		return p.BargeInSpeakerVerdictMaxAge
	}
	return bargeInSpeakerVerdictMaxAgeDefault
}

// bargeInTailGuard returns Pipeline.BargeInTailGuard when set (positive),
// otherwise the built-in default. Mirrors stallTimeout's override pattern in
// watchdog.go.
func (p *Pipeline) bargeInTailGuard() time.Duration {
	if p.BargeInTailGuard > 0 {
		return p.BargeInTailGuard
	}
	return bargeInPauseTailGuardDefault
}

// enabled reports whether every collaborator required for barge-in is present.
func (c *interruptController) enabled() bool {
	return c.capture != nil && c.vad != nil && c.playback != nil
}

// allowsInterrupt reports whether the current speaker verdict permits a trip
// (D7, review #13/#14). It fails open (true) whenever speakerVerdict is nil
// (barge_in_speakers=anyone: never consulted), the analyzer is not
// confidently healthy, no window verdict has arrived yet (cold start), or
// the verdict is older than verdictMaxAge (stale — including a verdict whose
// At was never stamped, since time.Since of a zero Time is always large).
// Only a confident, current, non-enrolled verdict blocks (returns false) —
// same posture as ownerVerifyGate: a privacy guard, not an auth boundary.
func (c *interruptController) allowsInterrupt() bool {
	if c.speakerVerdict == nil {
		return true
	}
	v := c.speakerVerdict()
	if !v.Healthy || !v.HasVerdict {
		return true
	}
	if c.verdictMaxAge > 0 && time.Since(v.At) > c.verdictMaxAge {
		return true
	}
	return v.Enrolled
}

// watch subscribes to capture and reports one barge-in request once sustained
// speech is detected, but only after playback is active and the arm window
// (tracked via armAt) has elapsed. The returned channel is nil when barge-in is
// disabled. The goroutine always unsubscribes from capture and clears the VAD on
// exit, and stops promptly on ctx cancellation.
func (c *interruptController) watch(ctx context.Context, armAt *atomic.Int64) <-chan interruptRequest {
	return c.watchWithDone(ctx, armAt).requests
}

func (c *interruptController) watchWithDone(ctx context.Context, armAt *atomic.Int64) interruptWatch {
	if !c.enabled() {
		return interruptWatch{}
	}

	out := make(chan interruptRequest, 1)
	done := make(chan struct{})
	subscriptionID, chunks := c.capture.Subscribe(c.bufferSize)

	go func() {
		defer close(done)
		defer c.capture.Unsubscribe(subscriptionID)
		defer c.vad.Clear()

		consecutiveSpeech := 0
		var audibility stt.SpeechAudibility
		segmentQueued := false
		var gapStart time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case samples, ok := <-chunks:
				if !ok {
					return
				}

				// Unarmed: the turn's first playback hasn't happened (armAt
				// zero — thinking-phase barge-in is a separate product call,
				// F3.4) or we're still inside the once-per-turn arm delay.
				// Reset so residual echo never accumulates toward a trip.
				at := armAt.Load()
				if at == 0 || time.Now().UnixNano() < at {
					consecutiveSpeech = 0
					audibility.Reset()
					segmentQueued = false
					c.vad.Clear()
					continue
				}

				// Armed for the rest of the turn. Playback pauses no longer
				// disarm (WI-dc9e33 B3): an utterance spanning a queue-drain
				// gap keeps its streak instead of restarting from zero. Only
				// the brief echo tail right after playback stops is skipped —
				// skipped, not cleared, so a spanning streak survives it.
				if c.playback.IsPlaying() {
					gapStart = time.Time{}
				} else {
					now := time.Now()
					if gapStart.IsZero() {
						gapStart = now
					}
					if now.Sub(gapStart) < c.tailGuard {
						continue
					}
				}

				c.vad.AcceptWaveform(samples)
				speech := c.vad.IsSpeech()
				if speech {
					consecutiveSpeech++
					gain := 1.0
					if reporter, ok := c.capture.(audio.GainReporter); ok {
						gain = reporter.InputGain()
					}
					audibility.Observe(samples, gain)
					if audibility.Audible() && !gapStart.IsZero() {
						c.lastPauseSpeech.Store(time.Now().UnixNano())
					}
				}

				// A completed segment can arrive on the first non-speech frame.
				// Judge it using its originating run before clearing that run.
				// A queued segment remains latched in the detector: only its
				// arrival may use this shortcut, never a later unrelated run.
				queued := c.vad.IsSpeechDetected()
				completed := queued && !segmentQueued
				segmentQueued = queued
				admit := audibility.Audible() && (completed || consecutiveSpeech >= c.minSpeech)
				if !speech {
					consecutiveSpeech = 0
					audibility.Reset()
				}

				if admit {
					if !c.allowsInterrupt() {
						// Confident non-enrolled speaker under
						// barge_in_speakers=enrolled_only: withhold this
						// trip but keep watching — the streak is left
						// alone, so an enrolled speaker joining a moment
						// later still trips immediately, and the same
						// stranger continuing to talk is re-checked on
						// every subsequent chunk.
						continue
					}
					select {
					case out <- interruptRequest{Reason: "barge_in", At: time.Now()}:
					default:
					}
					return
				}
			}
		}
	}()

	return interruptWatch{requests: out, done: done}
}

// sawPauseSpeechWithin reports whether the watcher heard speech during a
// playback pause within the last window — the F3.3 near-miss signal: real user
// speech that never reached the trip threshold but is sitting in the capture
// ring buffer. Only meaningful after the watcher goroutine has been joined.
func (c *interruptController) sawPauseSpeechWithin(window time.Duration) bool {
	at := c.lastPauseSpeech.Load()
	if at == 0 {
		return false
	}
	return time.Since(time.Unix(0, at)) <= window
}
