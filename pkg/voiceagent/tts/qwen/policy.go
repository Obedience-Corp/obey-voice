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

// SpeechWarm reports only a current session that completed a prior request.
func (q *Qwen3TTS) SpeechWarm() bool { return q.hasLiveSession() && q.speechCompleted.Load() }

func (q *Qwen3TTS) SpeechWarmState() string {
	if q == nil || (!q.native && !q.managed) {
		return "unknown"
	}
	if !q.hasLiveSession() {
		return "cold"
	}
	if q.speechCompleted.Load() {
		return "warm"
	}
	return "cold"
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

// liveSession reads the current speech transport, which a worker restart
// replaces under the caller.
func (q *Qwen3TTS) liveSession() qwenSpeechSession {
	if q == nil {
		return nil
	}
	if ref := q.speechRef.Load(); ref != nil && *ref != nil {
		return *ref
	}
	return nil
}
