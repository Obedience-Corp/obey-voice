package qwen

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// ModelTier reports the normalized model size (0.6b / 1.7b) so an artifact
// records which one produced the numbers.
//
// Over LAN the local qwen_tts_model_tier describes a worker this process never
// started, so the tier the speech worker reported about itself wins when it
// reported one (spec/30 §2 makes it optional). Falling back to the local key
// there would attribute the numbers to the wrong model.
func (q *Qwen3TTS) ModelTier() string {
	if q == nil {
		return ""
	}
	if q.remote {
		if tier := q.workerTier(); tier != "" {
			return managedqwen.NormalizeModelTier(tier)
		}
		return ""
	}
	return managedqwen.NormalizeModelTier(q.modelTier)
}

// SpeechPlace reports which computer synthesizes for this provider.
func (q *Qwen3TTS) SpeechPlace() string {
	if q != nil && q.remote {
		return tts.PlaceLAN
	}
	return tts.PlaceLocal
}

// WorkerStreaming reports whether this session's PCM actually arrives during
// synthesis: the worker can stream and this host asked it to. Stage A workers,
// and capable workers this host has decided not to stream from, answer false —
// for both, time-to-first-audio is the whole synthesis and no earlier, which
// is what a latency artifact needs to know.
func (q *Qwen3TTS) WorkerStreaming() bool {
	return streamRequestedOn(q.liveSession())
}

// SpeechWarm reports only current sessions that completed a prior request. A
// pool is warm when every engine is: the next segment may land on any of them,
// and one that still has to start or still has to pay its first request is a
// cold segment waiting to happen.
func (q *Qwen3TTS) SpeechWarm() bool { return q.hasLiveSession() && q.poolCompleted() }

func (q *Qwen3TTS) SpeechWarmState() string {
	if q == nil || (!q.native && !q.managed) {
		return "unknown"
	}
	if !q.hasLiveSession() {
		return "cold"
	}
	if q.poolCompleted() {
		return "warm"
	}
	return "cold"
}

// SegmentRamp is 1/1.83: in-pipeline RTF is 1.83 at 0.6b (tts-bench), and the
// ramp is the no-gap bound RTF x audio(k+1) <= audio(k), so it is 1/RTF like
// every other engine's — not 1.0. Synthesis here already costs more than the
// audio it produces, so the next segment has to be a little over half the one
// playing or the listener hears the difference. 1.7b is worse (2.2-2.6); the
// tier that is slower than its own ramp is a throughput problem, not a
// chunking one.
func (q *Qwen3TTS) SegmentRamp() float64 { return 0.55 }

// JoinsSentences is true. Decoding is autoregressive over the whole request,
// so a second sentence in the same call is conditioned on the first and the
// per-request fixed cost is paid once. Punctuation stays as written.
func (q *Qwen3TTS) JoinsSentences() bool { return true }

// Concurrency is how many worker subprocesses this provider owns
// (qwen_tts_workers, default 1). One worker holds its session mutex for a
// whole request, so the count is engine instances and never goroutines.
//
// Only the local native pool answers above 1: managed Python is a single
// legacy session, and a LAN worker's parallelism is the speech host's
// decision rather than this client's.
func (q *Qwen3TTS) Concurrency() int {
	if q == nil || !q.native || q.remote || len(q.pool) == 0 {
		return 1
	}
	return len(q.pool)
}

// WholeSentencePlayback is supported by Qwen's stream=false policy (including
// workers that do not advertise incremental PCM). The diagnostic uses this as
// a capability fact and never treats it as a promise of faster speech.
func (q *Qwen3TTS) WholeSentencePlayback() bool { return true }

// applyStreamPolicy tells a freshly-opened session whether to ask for chunks.
// It runs after every handshake — a worker restart re-decides rather than
// inheriting, because the replacement worker answers the capability question
// for itself.
func (q *Qwen3TTS) applyStreamPolicy(session qwenSpeechSession) {
	applier, ok := session.(interface {
		applyStreamPolicy(bool)
		StreamingCapable() bool
	})
	if !ok {
		return
	}
	applier.applyStreamPolicy(config.QwenStreamEligible(
		q.streamMode, q.remote, q.backendHint, applier.StreamingCapable(),
	))
}

// streamRequestedOn reports whether a session is asking its worker for chunks.
// A transport that cannot answer (the managed fixture sessions) is not
// streaming, which is the truth about it rather than a guess.
func streamRequestedOn(session qwenSpeechSession) bool {
	reporter, ok := session.(interface{ StreamRequested() bool })
	return ok && reporter.StreamRequested()
}

// workerTier is the tier the speech worker named for itself, when the
// transport can carry one. The local child never sends it.
func (q *Qwen3TTS) workerTier() string {
	session := q.liveSession()
	if session == nil {
		return ""
	}
	reporter, ok := session.(interface{ Tier() string })
	if !ok {
		return ""
	}
	return reporter.Tier()
}

// liveSession reads a current speech transport, which a worker restart
// replaces under the caller. Any live engine answers the questions that are
// about the transport itself — presets, tier, whether chunks are requested —
// because every engine in a pool is started from the same configuration.
func (q *Qwen3TTS) liveSession() qwenSpeechSession {
	if q == nil {
		return nil
	}
	for _, slot := range q.pool {
		if session := slot.live(); session != nil {
			return session
		}
	}
	return nil
}
