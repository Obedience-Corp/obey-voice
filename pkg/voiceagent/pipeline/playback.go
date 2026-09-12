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
	engine        ttsEngineIdentity
	gapElapsed    time.Duration
	gapCause      string
	gaps          []events.SpeechGap
	queuedAt      time.Time
	synthStarted  time.Time
	synthFirstPCM time.Time
	synthEnded    time.Time
	playStarted   time.Time
	playEnded     time.Time
	warmState     string
	wholeSentence bool
	textReady     time.Time
	exclusions    []string
	error         string
}

type playbackObservation struct {
	engine        ttsEngineIdentity
	textReady     time.Time
	queuedAt      time.Time
	synthStarted  time.Time
	stream        *audio.PCMStream
	warmState     string
	wholeSentence bool
	exclusions    []string
}

// applyPlaybackEvent applies one playback lifecycle event (produced by the
// watchPlayback watcher) to the turn's metrics and events. It returns true when
// the event marks a finished playback so the caller can decrement its pending
// count.
func (p *Pipeline) applyPlaybackEvent(event playbackEvent, metrics *turnMetrics, armAt *atomic.Int64) (finished bool) {
	if metrics.delivery != nil {
		metrics.delivery.observe(event, p.sinkKind())
	}
	switch event.kind {
	case playbackNotEnqueued:
		if event.error != "" {
			metrics.recordSegment(event)
		}
		return true

	case playbackStarted:
		// Arm once per turn, on the first playback: re-arming per sentence left
		// short sentences uninterruptible and re-imposed the delay across every
		// queue-drain gap (WI-dc9e33 B3 / F3.2).
		now := event.playStarted
		if now.IsZero() {
			now = time.Now()
		}
		armAt.CompareAndSwap(0, now.Add(bargeInArmDelay).UnixNano())
		// Re-check sink at pcmReady (PlayStream already returned) so a Stage
		// that opted in during generate still names AudibleSink / TTFA.
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = now
			if metrics.synthFirstFrame.IsZero() && !event.synthFirstPCM.IsZero() {
				metrics.synthFirstFrame = event.synthFirstPCM
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
			gap := now.Sub(metrics.lastPlaybackEnd)
			if gap < 0 {
				gap = 0
			}
			metrics.deadAir += gap
			event.gapElapsed = gap
			gapStart := metrics.lastPlaybackEnd
			event.gaps = partitionSpeechGap(gapStart, now, event)
			for i := range event.gaps {
				event.gaps[i].StartElapsed = metrics.elapsed(gapStart.Add(event.gaps[i].StartElapsed))
				event.gaps[i].EndElapsed = metrics.elapsed(gapStart.Add(event.gaps[i].EndElapsed))
			}
			event.gapCause = speechGapUnknown
			if len(event.gaps) == 1 && event.gaps[0].Cause != speechGapUnknown {
				event.gapCause = event.gaps[0].Cause
			}
			if metrics.gaps == nil {
				metrics.gaps = make(map[int]gapObservation)
			}
			metrics.gaps[event.index] = gapObservation{elapsed: event.gapElapsed, cause: event.gapCause, gaps: event.gaps}
			metrics.lastPlaybackEnd = time.Time{}
		}
		p.emitResponse(metrics, events.VoiceGenerated{
			Sentence: event.sentence,
			Elapsed:  event.synthElapsed,
			Index:    event.index,
			Fallback: event.fallback,
		})
		started := events.SpeakingStarted{Text: event.sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emitResponse(metrics, started)
		return false

	case playbackFinished:
		metrics.playbackComplete = event.playEnded
		if metrics.playbackComplete.IsZero() {
			metrics.playbackComplete = time.Now()
		}
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
			p.emitResponse(metrics, complete)
			return true
		}
		if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", event.result.Err)})
		}
		p.emitResponse(metrics, complete)
		return true
	}
	return false
}

// handlePlaybackLifecycle drives one utterance queued outside the turn loop —
// today only the degraded turn's recovery line. Its segment is recorded like
// any other: a degraded turn that spoke still paid for the synthesis, and a
// metrics record showing zero segments beside audible speech is wrong.
func (p *Pipeline) handlePlaybackLifecycle(sentence string, fallback bool, observation synthesisObservation, playback *audio.Playback, metrics *turnMetrics) {
	synthStarted := observation.synthStarted
	engineElapsed := time.Duration(0)
	if !synthStarted.IsZero() {
		engineElapsed = time.Since(synthStarted)
	}
	if metrics != nil && metrics.ttsEngine.provider == "" {
		metrics.ttsEngine = observation.engine
	}
	var startedAt time.Time
	synthElapsed := time.Duration(0)
	// Index continues the turn's own numbering: recovery speech follows
	// whatever the reply managed to say.
	index := len(metrics.segments)

	select {
	case <-playback.Started():
		startedAt = time.Now()
		synthElapsed = elapsedSince(synthStarted)
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = startedAt
		}
		if metrics.synthFirstFrame.IsZero() {
			if firstPCM := firstPCMAt(observation.stream); !firstPCM.IsZero() {
				metrics.synthFirstFrame = firstPCM
			}
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = startedAt
		}
		p.stampTTFA(metrics, startedAt)
		p.emitResponse(metrics, events.VoiceGenerated{
			Sentence: sentence,
			Elapsed:  synthElapsed,
			Index:    index,
			Fallback: fallback,
		})
		started := events.SpeakingStarted{Text: sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emitResponse(metrics, started)
	case result := <-playback.Done():
		if metrics.delivery != nil {
			metrics.delivery.observe(playbackEvent{kind: playbackFinished, index: index, result: result}, p.sinkKind())
		}
		if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
		}
		endedAt := time.Now()
		metrics.playbackComplete = endedAt
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
			engine:        observation.engine,
			queuedAt:      synthStarted,
			synthStarted:  synthStarted,
			synthEnded:    synthesisEnd(observation.stream, result),
			playEnded:     endedAt,
			textReady:     observation.textReady,
			warmState:     synthesisWarmState(observation.stream, observation.warmState),
			wholeSentence: observation.wholeSentence,
			exclusions:    append([]string(nil), observation.exclusions...),
			error:         playbackResultError(result),
		})
		p.emitResponse(metrics, events.SpeakingComplete{Interrupted: result.Interrupted, AudibleSink: metrics.audibleSink})
		return
	}

	result := <-playback.Done()
	if metrics.delivery != nil {
		metrics.delivery.observe(playbackEvent{kind: playbackFinished, index: index, result: result}, p.sinkKind())
	}
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
		engine:        observation.engine,
		queuedAt:      synthStarted,
		synthStarted:  synthStarted,
		synthFirstPCM: firstPCMAt(observation.stream),
		synthEnded:    synthesisEnd(observation.stream, result),
		playStarted:   startedAt,
		playEnded:     time.Now(),
		textReady:     observation.textReady,
		warmState:     synthesisWarmState(observation.stream, observation.warmState),
		wholeSentence: observation.wholeSentence,
		exclusions:    append([]string(nil), observation.exclusions...),
	})

	if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
	}
	if result.Interrupted {
		p.emitResponse(metrics, events.SpeakingInterrupted{Reason: "stopped"})
	}
	metrics.playbackComplete = time.Now()
	p.maybeStampLateTTFA(metrics)
	if metrics.audibleSink == audio.SinkNone {
		p.emit(events.Info{Message: "synth drain (no sink)"})
	}
	p.emitResponse(metrics, events.SpeakingComplete{
		Elapsed:     elapsed,
		Interrupted: result.Interrupted,
		AudibleSink: metrics.audibleSink,
	})
}

func elapsedSince(at time.Time) time.Duration {
	if at.IsZero() {
		return 0
	}
	return time.Since(at)
}

// watchPlayback is the playback watcher: it turns one playback's lifecycle into
// playbackStarted/playbackFinished events on out. Every send selects on loopDone
// (closed when streamResponse stops draining) so a watcher can never block — and
// leak — after the loop has returned on cancel, stall, or model error. loopDone
// is deliberately not the turn context: a barge-in cancels the turn context but
// the loop keeps consuming, so finished events must still be delivered to drain
// the pending count.
func watchPlayback(loopDone <-chan struct{}, sentence string, index int, fallback bool, observation playbackObservation, engineElapsed time.Duration, playback *audio.Playback, started *atomic.Bool, out chan<- playbackEvent) {
	engine := observation.engine
	queuedAt := observation.queuedAt
	synthStarted := observation.synthStarted
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
			engine:        engine,
			textReady:     observation.textReady,
			queuedAt:      queuedAt,
			synthStarted:  synthStarted,
			synthFirstPCM: firstPCMAt(observation.stream),
			synthEnded:    synthesisEnd(observation.stream, audio.PlaybackResult{}),
			playStarted:   startedAt,
			warmState:     synthesisWarmState(observation.stream, observation.warmState),
			wholeSentence: observation.wholeSentence,
			exclusions:    append([]string(nil), observation.exclusions...),
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
			engine:        engine,
			textReady:     observation.textReady,
			queuedAt:      queuedAt,
			synthStarted:  synthStarted,
			synthFirstPCM: firstPCMAt(observation.stream),
			synthEnded:    synthesisEnd(observation.stream, result),
			warmState:     synthesisWarmState(observation.stream, observation.warmState),
			wholeSentence: observation.wholeSentence,
			exclusions:    append([]string(nil), observation.exclusions...),
			error:         playbackResultError(result),
			playEnded:     time.Now(),
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
		engine:        engine,
		textReady:     observation.textReady,
		queuedAt:      queuedAt,
		synthStarted:  synthStarted,
		synthFirstPCM: firstPCMAt(observation.stream),
		synthEnded:    synthesisEnd(observation.stream, result),
		playStarted:   startedAt,
		playEnded:     time.Now(),
		warmState:     synthesisWarmState(observation.stream, observation.warmState),
		wholeSentence: observation.wholeSentence,
		exclusions:    append([]string(nil), observation.exclusions...),
		error:         playbackResultError(result),
	})
}

func playbackResultError(result audio.PlaybackResult) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	return ""
}

// A provider can restart after the initial observation. Prefer the evidence
// attached to the actual request when recording its terminal segment.
func synthesisWarmState(stream *audio.PCMStream, initial string) string {
	if stream != nil {
		switch state := stream.SynthesisWarmState(); state {
		case events.WarmStateWarm, events.WarmStateCold, events.WarmStateUnknown:
			return state
		}
	}
	return initial
}

func firstPCMAt(stream *audio.PCMStream) time.Time {
	if stream == nil {
		return time.Time{}
	}
	return stream.FirstWrittenAt()
}

func synthesisEnd(stream *audio.PCMStream, result audio.PlaybackResult) time.Time {
	if stream != nil {
		if closed := stream.ClosedAt(); !closed.IsZero() {
			return closed
		}
	}
	if !result.SynthesisEndedAt.IsZero() {
		return result.SynthesisEndedAt
	}
	return time.Time{}
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

// partitionSpeechGap attributes only directly observed, non-overlapping
// portions of the idle interval. Returned offsets are relative to start;
// callers convert them to the turn's elapsed clock before serialization.
func partitionSpeechGap(start, end time.Time, event playbackEvent) []events.SpeechGap {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return nil
	}
	type candidate struct {
		start time.Time
		end   time.Time
		cause string
	}
	candidates := make([]candidate, 0, 3)
	if !event.textReady.IsZero() {
		candidates = append(candidates, candidate{start: start, end: event.textReady, cause: speechGapWaitingText})
	}
	if !event.queuedAt.IsZero() && !event.synthStarted.IsZero() {
		candidates = append(candidates, candidate{start: event.queuedAt, end: event.synthStarted, cause: speechGapSynthQueue})
	}
	if !event.engine.workerStreaming && event.engine.place == "local" &&
		!event.synthStarted.IsZero() && !event.synthEnded.IsZero() {
		candidates = append(candidates, candidate{start: event.synthStarted, end: event.synthEnded, cause: speechGapSynthesis})
	}
	// Invalid observed boundaries cannot support a stage attribution. Retain
	// the raw gap as unknown rather than repairing a producer timestamp.
	for _, c := range candidates {
		if !c.end.After(c.start) {
			return unknownSpeechGap(end.Sub(start))
		}
	}
	// Sort the small candidate set by observed start time.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].start.Before(candidates[j-1].start); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
	clipped := candidates[:0]
	for _, c := range candidates {
		if !c.end.After(start) || !c.start.Before(end) {
			continue
		}
		if c.start.Before(start) {
			c.start = start
		}
		if c.end.After(end) {
			c.end = end
		}
		if c.end.After(c.start) {
			clipped = append(clipped, c)
		}
	}
	for i := 1; i < len(clipped); i++ {
		if clipped[i].start.Before(clipped[i-1].end) {
			return unknownSpeechGap(end.Sub(start))
		}
	}
	candidates = clipped
	result := make([]events.SpeechGap, 0, len(candidates)+1)
	cursor := start
	for _, c := range candidates {
		if c.end.Before(start) || !c.end.After(c.start) || !c.start.Before(end) {
			continue
		}
		if c.start.Before(start) {
			c.start = start
		}
		if c.end.After(end) {
			c.end = end
		}
		if !c.end.After(c.start) {
			continue
		}
		if c.start.After(cursor) {
			result = append(result, events.SpeechGap{StartElapsed: cursor.Sub(start), EndElapsed: c.start.Sub(start), Cause: speechGapUnknown})
		}
		result = append(result, events.SpeechGap{StartElapsed: c.start.Sub(start), EndElapsed: c.end.Sub(start), Cause: c.cause})
		if c.end.After(cursor) {
			cursor = c.end
		}
	}
	if cursor.Before(end) {
		result = append(result, events.SpeechGap{StartElapsed: cursor.Sub(start), EndElapsed: end.Sub(start), Cause: speechGapUnknown})
	}
	return result
}

func unknownSpeechGap(duration time.Duration) []events.SpeechGap {
	if duration <= 0 {
		return nil
	}
	return []events.SpeechGap{{EndElapsed: duration, Cause: speechGapUnknown}}
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
