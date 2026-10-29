package qwen

import (
	"context"
	"errors"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// synthesizeNativeStream runs one request against a free engine and restarts
// that engine once on unexpected process/protocol failure (same policy as
// managed).
//
// The slot is the unit of serialization: one worker still takes one request at
// a time, and a worker that dies takes only its own slot down — the rest of
// the pool keeps serving.
func (q *Qwen3TTS) synthesizeNativeStream(ctx context.Context, req tts.SynthesisRequest, stream *audio.PCMStream) error {
	slot := q.acquireSlot()
	defer slot.mu.Unlock()

	if !q.alive.Load() {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if err := q.ensureSpeechSessionLocked(ctx, slot); err != nil {
		return err
	}
	if slot.completed.Load() {
		stream.SetSynthesisWarmState("warm")
	} else {
		stream.SetSynthesisWarmState("cold")
	}

	// Declared before the first frame: the player reads it once, when it takes
	// the segment. A session that is not streaming leaves it false, so the
	// whole-utterance path is unchanged down to the byte.
	stream.SetIncremental(streamRequestedOn(slot.session))

	err := slot.session.SynthesizeToStream(ctx, req, stream)
	if err == nil {
		slot.completed.Store(true)
		return nil
	}
	q.discardSpeechSessionLocked(slot)
	if ctx.Err() != nil || !managedWorkerRestartable(err) {
		return err
	}
	stream.SetSynthesisWarmState("cold")
	// Retrying is only free while nothing has been heard. Once samples have
	// been handed to the player, part of the sentence is already in front of
	// the listener, and synthesizing it again from the top would append a
	// second copy to the same stream — the sentence spoken twice, concatenated.
	// So the segment ends here: the listener hears a truncated sentence, the
	// pipeline surfaces the error, and nothing is repeated. The turn's Kokoro
	// fallback does not cover this — playFallback only runs for a failure
	// raised before the player owns the stream — which is the right trade,
	// because the alternative is saying half a sentence twice.
	if stream.Written() > 0 {
		return fmt.Errorf("%s failed after %d samples were already playing: %w", q.speechLabel(), stream.Written(), err)
	}
	if restartErr := q.ensureSpeechSessionLocked(ctx, slot); restartErr != nil {
		return fmt.Errorf("%s failed (%v); reconnect failed: %w", q.speechLabel(), err, restartErr)
	}
	// The replacement worker answers the capability question for itself, so the
	// stream has to be re-declared against it rather than left describing the
	// session that just died. Nothing has been written yet (guarded above), so
	// the player has not read the flag and a change here is still in time.
	stream.SetIncremental(streamRequestedOn(slot.session))
	if retryErr := slot.session.SynthesizeToStream(ctx, req, stream); retryErr != nil {
		q.discardSpeechSessionLocked(slot)
		return fmt.Errorf("%s failed after one reconnect: %w", q.speechLabel(), retryErr)
	}
	slot.completed.Store(true)
	return nil
}

// speechLabel names the transport in an operator-facing error.
func (q *Qwen3TTS) speechLabel() string {
	if q.remote {
		return "speech worker " + q.remoteAddr
	}
	return "native worker"
}

// ensureSpeechSessionLocked opens this slot's session if it has none. The
// caller holds slot.mu, so only this engine is affected.
func (q *Qwen3TTS) ensureSpeechSessionLocked(ctx context.Context, slot *speechSlot) error {
	if slot.session != nil {
		return nil
	}
	if q.startSpeech == nil {
		return errors.New("qwen3-tts: no speech transport configured")
	}
	session, err := q.startSpeech(ctx)
	if err != nil {
		if q.remote {
			return err
		}
		return fmt.Errorf("start native worker: %w", err)
	}
	q.applyStreamPolicy(session)
	slot.adopt(session)
	return nil
}

// discardSpeechSessionLocked closes this slot's worker and clears its warm
// evidence. The other slots keep serving.
func (q *Qwen3TTS) discardSpeechSessionLocked(slot *speechSlot) {
	slot.completed.Store(false)
	if slot.session == nil {
		return
	}
	slot.ref.Store(nil)
	slot.session.Close()
	slot.session = nil
}

// synthesizeManagedStream serializes access to the persistent model worker and
// owns its recovery policy. Cancellation invalidates only the worker process;
// the provider remains available and starts a fresh worker on the next request.
// Unexpected worker/protocol failures receive one immediate restart + retry so
// conversational callers reach their configured fallback only after Qwen has
// had a chance to recover itself.
func (q *Qwen3TTS) synthesizeManagedStream(ctx context.Context, req tts.SynthesisRequest, stream *audio.PCMStream) error {
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()

	if !q.alive.Load() {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if err := q.ensureManagedSessionLocked(ctx); err != nil {
		return err
	}
	if q.primarySlot().completed.Load() {
		stream.SetSynthesisWarmState("warm")
	} else {
		stream.SetSynthesisWarmState("cold")
	}

	err := q.session.SynthesizeToStream(ctx, req, stream)
	if err == nil {
		q.primarySlot().completed.Store(true)
		return nil
	}
	q.discardManagedSessionLocked()
	if ctx.Err() != nil || !managedWorkerRestartable(err) {
		return err
	}
	stream.SetSynthesisWarmState("cold")

	if restartErr := q.ensureManagedSessionLocked(ctx); restartErr != nil {
		return fmt.Errorf("managed worker failed (%v); restart failed: %w", err, restartErr)
	}
	if retryErr := q.session.SynthesizeToStream(ctx, req, stream); retryErr != nil {
		q.discardManagedSessionLocked()
		return fmt.Errorf("managed worker failed after one restart: %w", retryErr)
	}
	q.primarySlot().completed.Store(true)
	return nil
}

func (q *Qwen3TTS) ensureManagedSessionLocked(ctx context.Context) error {
	if q.session != nil {
		return nil
	}
	timeout := q.startupTimeout
	if timeout <= 0 {
		timeout = max(q.timeout, 10*time.Minute)
	}
	session, err := startManagedQwenSessionContext(ctx, q.binary, q.workerScript, q.model, timeout)
	if err != nil {
		return fmt.Errorf("start managed worker: %w", err)
	}
	q.session = session
	return nil
}

func (q *Qwen3TTS) discardManagedSessionLocked() {
	q.primarySlot().completed.Store(false)
	if q.session == nil {
		return
	}
	q.session.Close()
	q.session = nil
}
