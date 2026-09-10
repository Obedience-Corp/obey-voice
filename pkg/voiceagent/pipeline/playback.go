package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type playbackEventType int

const (
	playbackStarted playbackEventType = iota
	playbackFinished
	// playbackNotEnqueued reports a handed-off sentence that produced no
	// playback (TTS/playback error, or synthesis canceled), so the loop can
	// release its pending slot.
	playbackNotEnqueued
)

type playbackEvent struct {
	kind     playbackEventType
	sentence string
	index    int
	fallback bool
	// engineElapsed is synthesis alone; synthElapsed also covers waiting
	// behind whatever was already playing.
	engineElapsed time.Duration
	synthElapsed  time.Duration
	playElapsed   time.Duration
	result        audio.PlaybackResult
}

// applyPlaybackEvent applies one playback lifecycle event (produced by the
// watchPlayback watcher) to the turn's metrics and events. It returns true when
// the event marks a finished playback so the caller can decrement its pending
// count.
func (p *Pipeline) applyPlaybackEvent(event playbackEvent, metrics *turnMetrics, armAt *atomic.Int64) (finished bool) {
	switch event.kind {
	case playbackNotEnqueued:
		return true

	case playbackStarted:
		// Arm once per turn, on the first playback: re-arming per sentence left
		// short sentences uninterruptible and re-imposed the delay across every
		// queue-drain gap (WI-dc9e33 B3 / F3.2).
		now := time.Now()
		armAt.CompareAndSwap(0, now.Add(bargeInArmDelay).UnixNano())
		// Re-check sink at pcmReady (PlayStream already returned) so a Stage
		// that opted in during generate still names AudibleSink / TTFA.
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = now
			if metrics.synthFirstFrame.IsZero() {
				metrics.synthFirstFrame = now
			}
			// No sttFinal, no sample — deliberately. The only fallback stamp,
			// metrics.start, includes the user's own speaking time, so it would
			// inflate the rolling p50 and open the filler gate on turns that are
			// actually fast. Text turns never play filler, so their missing
			// sample costs nothing.
			if p.backchannel != nil && !metrics.sttFinal.IsZero() {
				p.backchannel.observe(metrics.firstAudioReady.Sub(metrics.sttFinal))
			}
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = now
		}
		p.stampTTFA(metrics, now)
		// The player went idle after the previous sentence and has only now
		// found the next one ready. That gap is audible silence.
		if !metrics.lastPlaybackEnd.IsZero() {
			metrics.deadAir += time.Since(metrics.lastPlaybackEnd)
			metrics.lastPlaybackEnd = time.Time{}
		}
		p.emit(events.VoiceGenerated{
			Sentence: event.sentence,
			Elapsed:  event.synthElapsed,
			Index:    event.index,
			Fallback: event.fallback,
		})
		started := events.SpeakingStarted{Text: event.sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emit(started)
		return false

	case playbackFinished:
		metrics.playbackComplete = time.Now()
		// Stamped unconditionally: if this was the turn's last segment the
		// stamp is simply never consumed, so trailing silence after the reply
		// is not counted as a gap inside it.
		metrics.lastPlaybackEnd = metrics.playbackComplete
		p.maybeStampLateTTFA(metrics)
		if metrics.audibleSink == audio.SinkNone && len(metrics.segments) == 0 {
			p.emit(events.Info{Message: "synth drain (no sink)"})
		}
		metrics.recordSegment(event)
		complete := events.SpeakingComplete{
			Elapsed:     event.playElapsed,
			Interrupted: event.result.Interrupted,
			AudibleSink: metrics.audibleSink,
		}
		if event.result.Interrupted {
			p.emit(complete)
			return true
		}
		if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", event.result.Err)})
		}
		p.emit(complete)
		return true
	}
	return false
}

// handlePlaybackLifecycle drives one utterance queued outside the turn loop —
// today only the degraded turn's recovery line. Its segment is recorded like
// any other: a degraded turn that spoke still paid for the synthesis, and a
// metrics record showing zero segments beside audible speech is wrong.
func (p *Pipeline) handlePlaybackLifecycle(sentence string, fallback bool, synthStarted time.Time, engineElapsed time.Duration, playback *audio.Playback, metrics *turnMetrics) {
	var startedAt time.Time
	synthElapsed := time.Duration(0)
	// Index continues the turn's own numbering: recovery speech follows
	// whatever the reply managed to say.
	index := len(metrics.segments)

	select {
	case <-playback.Started():
		startedAt = time.Now()
		synthElapsed = time.Since(synthStarted)
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = startedAt
		}
		if metrics.synthFirstFrame.IsZero() {
			metrics.synthFirstFrame = startedAt
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = startedAt
		}
		p.stampTTFA(metrics, startedAt)
		p.emit(events.VoiceGenerated{
			Sentence: sentence,
			Elapsed:  synthElapsed,
			Index:    index,
			Fallback: fallback,
		})
		started := events.SpeakingStarted{Text: sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emit(started)
	case result := <-playback.Done():
		if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
		}
		metrics.playbackComplete = time.Now()
		p.maybeStampLateTTFA(metrics)
		if metrics.audibleSink == audio.SinkNone {
			p.emit(events.Info{Message: "synth drain (no sink)"})
		}
		metrics.recordSegment(playbackEvent{
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: engineElapsed,
			synthElapsed:  time.Since(synthStarted),
			result:        result,
		})
		p.emit(events.SpeakingComplete{Interrupted: result.Interrupted, AudibleSink: metrics.audibleSink})
		return
	}

	result := <-playback.Done()
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}
	metrics.recordSegment(playbackEvent{
		sentence:      sentence,
		index:         index,
		fallback:      fallback,
		engineElapsed: engineElapsed,
		synthElapsed:  synthElapsed,
		playElapsed:   elapsed,
		result:        result,
	})

	if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
	}
	if result.Interrupted {
		p.emit(events.SpeakingInterrupted{Reason: "stopped"})
	}
	metrics.playbackComplete = time.Now()
	p.maybeStampLateTTFA(metrics)
	if metrics.audibleSink == audio.SinkNone {
		p.emit(events.Info{Message: "synth drain (no sink)"})
	}
	p.emit(events.SpeakingComplete{
		Elapsed:     elapsed,
		Interrupted: result.Interrupted,
		AudibleSink: metrics.audibleSink,
	})
}

// watchPlayback is the playback watcher: it turns one playback's lifecycle into
// playbackStarted/playbackFinished events on out. Every send selects on loopDone
// (closed when streamResponse stops draining) so a watcher can never block — and
// leak — after the loop has returned on cancel, stall, or model error. loopDone
// is deliberately not the turn context: a barge-in cancels the turn context but
// the loop keeps consuming, so finished events must still be delivered to drain
// the pending count.
func watchPlayback(loopDone <-chan struct{}, sentence string, index int, fallback bool, synthStarted time.Time, engineElapsed time.Duration, playback *audio.Playback, started *atomic.Bool, out chan<- playbackEvent) {
	var startedAt time.Time
	// synthElapsed is stamped on both terminal paths, not just the audible
	// one: a segment whose playback finished without ever starting still cost
	// its synthesis time, and dropping it would silently bias the aggregate
	// toward the segments that went well.
	synthElapsed := time.Duration(0)

	select {
	case <-loopDone:
		return
	case <-playback.Started():
		started.Store(true)
		startedAt = time.Now()
		synthElapsed = time.Since(synthStarted)
		if !sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:          playbackStarted,
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: engineElapsed,
			synthElapsed:  synthElapsed,
		}) {
			return
		}
	case result := <-playback.Done():
		sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:          playbackFinished,
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: measuredEngineElapsed(synthStarted, engineElapsed, result),
			synthElapsed:  time.Since(synthStarted),
			result:        result,
		})
		return
	}

	var result audio.PlaybackResult
	select {
	case <-loopDone:
		return
	case result = <-playback.Done():
	}
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	sendPlaybackEvent(loopDone, out, playbackEvent{
		kind:          playbackFinished,
		sentence:      sentence,
		index:         index,
		fallback:      fallback,
		engineElapsed: measuredEngineElapsed(synthStarted, engineElapsed, result),
		synthElapsed:  synthElapsed,
		playElapsed:   elapsed,
		result:        result,
	})
}

// measuredEngineElapsed prefers the player's stamp of when the synthesizer
// stopped producing over the caller's own timing.
//
// They agree for a whole-utterance engine — PlayStream returns immediately
// after the stream closes — and they disagree by the length of the reply for a
// streamed one, which becomes audible on its first chunk. Without this the
// realtime factor of every streamed segment would read as a fraction of its
// true value, and RealtimeFactor is the number that decides whether an engine
// can keep up. A missing stamp (an interrupted segment, or a player that does
// not report one) keeps the caller's measurement.
func measuredEngineElapsed(synthStarted time.Time, fallback time.Duration, result audio.PlaybackResult) time.Duration {
	if result.SynthesisEndedAt.IsZero() {
		return fallback
	}
	measured := result.SynthesisEndedAt.Sub(synthStarted)
	if measured <= 0 {
		return fallback
	}
	return measured
}

// sendPlaybackEvent delivers ev unless the loop has stopped consuming, returning
// false if it abandoned the send.
func sendPlaybackEvent(loopDone <-chan struct{}, out chan<- playbackEvent, ev playbackEvent) bool {
	select {
	case <-loopDone:
		return false
	case out <- ev:
		return true
	}
}
