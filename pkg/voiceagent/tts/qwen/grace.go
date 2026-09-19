package qwen

import (
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"strings"
	"time"

	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// FirstAudioGrace reports the stall budget for a segment of unstated length.
// See FirstAudioGraceForChars, which is what the pipeline calls when it knows.
func (q *Qwen3TTS) FirstAudioGrace() time.Duration {
	return q.FirstAudioGraceForChars(tts.TypicalSegmentChars)
}

// FirstAudioGraceForChars reports how long the pipeline's playback-stall
// watchdog should wait for the first audible frame of a segment this long.
//
// Stage-A Qwen generates a whole utterance before exposing any PCM, so the
// wait is the synthesis: expected audio duration times the realtime factor
// the tier is expected to hold, plus a cold-start allowance while the worker
// is still coming up.
//
// A streaming session reaches its first audible frame far sooner than that, so
// this budget is generous rather than wrong there — and it stays deliberately
// sized for the whole utterance, because the same session falls back to Stage
// A whenever the worker turns out not to be capable. What streaming needs
// bounded is the *silence between chunks*, which is a different budget the
// pipeline arms separately (boundPrimarySilence).
//
// qwen_tts_timeout is a CAP here, not the budget. It bounds the synthesis call
// itself (see Synthesize, which wraps the request in a context with this
// timeout), so waiting past it cannot pay off — synthesis will already have
// failed. Adopting it as the budget, which this used to do, meant an operator
// who raised the synthesis ceiling for long-form audio silently raised the
// deadline for noticing a wedged player to the same number: a 120s
// qwen_tts_timeout bought a 120s stall grace, and two minutes of silence
// registered as a healthy turn.
func (q *Qwen3TTS) FirstAudioGraceForChars(chars int) time.Duration {
	if q == nil {
		return managedWarmTTP95
	}
	if !q.native {
		grace := managedWarmTTP95
		return q.capByTimeout(grace)
	}

	if chars <= 0 {
		chars = tts.TypicalSegmentChars
	}
	ceiling := q.rtfCeiling()
	expectedAudio := float64(chars) / tts.SpeechCharsPerSecond
	grace := time.Duration(ceiling * expectedAudio * float64(time.Second))

	// The cold-ready allowance is paid when a worker has to come up, not on
	// every turn — charging it always is what made short warm turns wait out
	// a worst-case budget.
	if !q.hasLiveSession() {
		grace += nativeColdReadyP95
	}
	return q.capByTimeout(grace)
}

// hasLiveSession reports whether a worker session is up right now, which is
// what decides whether the next synthesis pays a cold start.
//
// q.alive is the wrong signal, and using it was a bug. It means "this
// provider is usable", is set once during construction — before any synthesis
// — and deliberately survives discardSpeechSessionLocked so the provider can
// restart itself after a worker or protocol failure. Keyed to it, the
// allowance never fired on a genuinely cold first turn, and a segment landing
// on the restart path got the warm budget (often the 8s floor) while actually
// paying a full ~11s startup. That would let the stall watchdog cancel the
// self-healing restart it exists to protect.
// A pool answers true only when every worker is up. A segment dispatched to
// the one slot that still has to launch pays the full cold start, so charging
// the allowance for the whole pool is the direction that cannot cancel a
// healthy turn.
func (q *Qwen3TTS) hasLiveSession() bool {
	return q.poolLive()
}

// capByTimeout bounds a computed grace by the configured synthesis timeout,
// beyond which waiting is provably useless, and floors it so a very short
// segment still tolerates the tier's per-request cost plus scheduling jitter.
func (q *Qwen3TTS) capByTimeout(grace time.Duration) time.Duration {
	if min := q.minGrace(); grace < min {
		grace = min
	}
	if q.timeoutSet && q.timeout > 0 && grace > q.timeout {
		return q.timeout
	}
	return grace
}

// minGrace is the shortest stall budget this provider will accept. The 1.7b
// worker's per-request cost is larger than the 0.6b scheduler floor; using
// one number for both is how a selected 1.7b voice was replaced with Kokoro
// on a still-working short sentence.
func (q *Qwen3TTS) rtfCeiling() float64 {
	if q != nil && q.native && strings.EqualFold(q.backendHint, "cpu") {
		return nativeRTFCeilingCPU
	}
	if q != nil && managedqwen.NormalizeModelTier(q.modelTier) == managedqwen.Tier1_7B {
		return nativeRTFCeiling17B
	}
	return nativeRTFCeiling06B
}

func (q *Qwen3TTS) minGrace() time.Duration {
	if q != nil && q.native && strings.EqualFold(q.backendHint, "cpu") {
		return minFirstAudioGraceCPU
	}
	if q != nil && q.native && managedqwen.NormalizeModelTier(q.modelTier) == managedqwen.Tier1_7B {
		return minFirstAudioGrace17B
	}
	return minFirstAudioGrace
}
