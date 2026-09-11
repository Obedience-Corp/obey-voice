package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// softCancelTTS asks the primary provider to abandon in-flight synthesis without
// process kill when it implements tts.SoftCanceler (native qwen3-tts-worker).
func (p *Pipeline) softCancelTTS(reason string) {
	primary, _ := p.ttsProviders()
	if sc, ok := primary.(tts.SoftCanceler); ok {
		_ = sc.SoftCancel(reason)
	}
}

// discardPCMStream drains a synthesized stream that will not be played so the
// producer goroutine is not left blocked on a full frames channel. Drain runs
// asynchronously: the caller must not wait on synth completion after mute.
func discardPCMStream(stream *audio.PCMStream) {
	if stream == nil {
		return
	}
	go func() {
		for range stream.Frames() {
		}
	}()
}

// synthesizeSegment is the synth scheduler stage, run on the ordered synth
// worker: it announces the segment, synthesizes it, starts playback, and hands
// the playback to the watcher goroutine, returning true when a playback was
// enqueued so the loop can track it as pending. TTS/playback failures surface as
// Error events; failures after the turn context is canceled (cancel or barge-in)
// are swallowed so teardown stays quiet. It never blocks on a full queue —
// backpressure is the loop's job via the pending count.
//
// stickFallback, when non-nil, is turn-scoped sticky Kokoro fallback: after the
// first successful primary→fallback switch, later sentences skip the primary so
// one reply cannot alternate persona voice and Kokoro.
func (p *Pipeline) synthesizeSegment(ctx context.Context, loopDone <-chan struct{}, sentence string, index int, audioStarted *atomic.Bool, out chan<- playbackEvent, stickFallback *bool, realAudioQueued ...*atomic.Bool) bool {
	now := time.Now()
	return p.synthesizeSegmentObserved(ctx, loopDone, synthWork{sentence: sentence, textReady: now, queuedAt: now}, index, audioStarted, out, stickFallback, realAudioQueued...).enqueued
}

type segmentSynthesisOutcome struct {
	enqueued bool
	failure  *playbackEvent
}

type synthWork struct {
	sentence  string
	textReady time.Time
	queuedAt  time.Time
}

type synthesisObservation struct {
	engine        ttsEngineIdentity
	warmState     string
	wholeSentence bool
	exclusions    []string
	stream        *audio.PCMStream
	textReady     time.Time
	queuedAt      time.Time
	synthStarted  time.Time
}

func (p *Pipeline) synthesizeSegmentObserved(ctx context.Context, loopDone <-chan struct{}, work synthWork, index int, audioStarted *atomic.Bool, out chan<- playbackEvent, stickFallback *bool, realAudioQueued ...*atomic.Bool) segmentSynthesisOutcome {
	sentence := work.sentence
	if ctx.Err() != nil || p.OutputMuted() {
		return segmentSynthesisOutcome{} // canceled while queued: drain without synthesizing
	}
	if work.queuedAt.IsZero() {
		work.queuedAt = time.Now()
	}
	seg := events.SpeechSegmentReady{Text: sentence}
	if index == 0 {
		p.activeMu.Lock()
		m := p.activeMetrics
		p.activeMu.Unlock()
		if m != nil && !m.firstAdmit.IsZero() && !m.firstAdmit.Equal(m.firstSpeakable) {
			seg.Elapsed = m.elapsedFromEOU(m.firstAdmit)
		}
	}
	p.emitResponseContext(ctx, seg)
	p.emitResponseContext(ctx, events.GeneratingVoice{Sentence: sentence})

	synthStarted := time.Now()
	stream, usedFallback, observation, err := p.synthUtteranceObserved(ctx, sentence, stickFallback)
	if err != nil {
		if ctx.Err() == nil {
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		}
		if observation.synthStarted.IsZero() {
			return segmentSynthesisOutcome{}
		}
		observation.textReady = work.textReady
		observation.queuedAt = work.queuedAt
		failure := synthesisFailureEvent(sentence, index, observation, err)
		failure.fallback = usedFallback
		return segmentSynthesisOutcome{failure: failure}
	}
	if observation.synthStarted.IsZero() {
		observation.synthStarted = synthStarted
	}
	synthStarted = observation.synthStarted
	observation.textReady = work.textReady
	observation.queuedAt = work.queuedAt
	if observation.stream == nil {
		observation.stream = stream
	}
	if p.OutputMuted() {
		// Synthesize already started a producer goroutine. Drain so it is not
		// left blocked on the buffered frames channel until turn cancel.
		discardPCMStream(stream)
		return segmentSynthesisOutcome{}
	}

	firstRealAudio := true
	if len(realAudioQueued) > 0 && realAudioQueued[0] != nil {
		firstRealAudio = realAudioQueued[0].CompareAndSwap(false, true)
	}
	if firstRealAudio {
		p.stopBackchannel()
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		if !usedFallback {
			var fallbackObservation synthesisObservation
			playback, usedFallback, fallbackObservation, err = p.playFallbackObserved(ctx, sentence, err, stickFallback)
			if usedFallback {
				observation = fallbackObservation
			}
		}
		if err == nil {
			// The fallback playback is ready; continue to enqueue its watcher.
		} else {
			// PlayStream owns stream once invoked (real Player pumps even when
			// waitReady fails); do not discard here or two consumers race.
			if ctx.Err() == nil {
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
			}
			observation.textReady = work.textReady
			observation.queuedAt = work.queuedAt
			failure := synthesisFailureEvent(sentence, index, observation, err)
			failure.fallback = usedFallback
			return segmentSynthesisOutcome{failure: failure}
		}
	}
	// Player failure may replace the primary stream with a fallback stream.
	// Keep queue/text-ready boundaries from work, but use the final provider's
	// start and stream lifecycle for synthesis telemetry.
	if observation.stream != nil && observation.stream != stream {
		stream = observation.stream
		synthStarted = observation.synthStarted
	}
	observation.textReady = work.textReady
	observation.queuedAt = work.queuedAt
	if observation.stream == nil {
		observation.stream = stream
	}
	// For a whole-utterance engine PlayStream returns once the audio is
	// buffered, so this is where synthesis ended. A streamed segment becomes
	// audible long before that; watchPlayback replaces this with the player's
	// own stamp of when the last frame arrived.
	engineElapsed := time.Since(synthStarted)

	// PlayStream buffers a complete sentence before enqueueing it. Muting can
	// race that wait, so the initial Stop may see nothing; close the race by
	// checking once more after enqueue and stopping the late segment.
	if p.OutputMuted() {
		p.Player.Stop()
		return segmentSynthesisOutcome{}
	}

	go watchPlayback(loopDone, sentence, index, usedFallback, playbackObservation{
		engine: observation.engine, textReady: work.textReady, queuedAt: work.queuedAt,
		synthStarted: synthStarted, stream: stream,
		warmState: observation.warmState, wholeSentence: observation.wholeSentence,
		exclusions: observation.exclusions,
	}, engineElapsed, playback, audioStarted, out)
	return segmentSynthesisOutcome{enqueued: true}
}

func synthesisFailureEvent(sentence string, index int, observation synthesisObservation, err error) *playbackEvent {
	event := &playbackEvent{
		kind: playbackNotEnqueued, sentence: sentence, index: index,
		engine: observation.engine, textReady: observation.textReady,
		queuedAt: observation.queuedAt, synthStarted: observation.synthStarted,
		warmState: synthesisWarmState(observation.stream, observation.warmState), wholeSentence: observation.wholeSentence,
		synthFirstPCM: firstPCMAt(observation.stream), synthEnded: synthesisEnd(observation.stream, audio.PlaybackResult{}),
		exclusions: append([]string(nil), observation.exclusions...),
	}
	if !event.synthStarted.IsZero() && event.synthEnded.After(event.synthStarted) {
		event.engineElapsed = event.synthEnded.Sub(event.synthStarted)
	}
	if err != nil {
		event.error = err.Error()
	}
	return event
}

// synthesizeWithFallback retries a synchronous provider failure once with the
// configured fallback. A Qwen worker failure is surfaced by PlayStream instead
// and is handled by playFallback below because Player owns that stream.
//
// stickFallback, when non-nil, latches true after the first successful switch so
// later sentences in the same turn stay on Kokoro (one voice switch max per
// reply, not uncle-fu / af_heart / uncle-fu thrashing).
func (p *Pipeline) synthesizeWithFallback(ctx context.Context, text string, stickFallback *bool) (*audio.PCMStream, bool, error) {
	stream, fallback, _, err := p.synthesizeWithFallbackObserved(ctx, text, stickFallback)
	return stream, fallback, err
}

func (p *Pipeline) synthesizeWithFallbackObserved(ctx context.Context, text string, stickFallback *bool) (*audio.PCMStream, bool, synthesisObservation, error) {
	primary, fallback := p.responseTTS(ctx)
	if primary == nil {
		return nil, false, synthesisObservation{warmState: events.WarmStateUnknown}, errors.New("TTS provider is not configured")
	}
	if stickFallback != nil && *stickFallback && p.canUseFallback(ctx, primary, fallback) {
		observation := p.observeProvider(fallback)
		observation.synthStarted = time.Now()
		fallbackStream, fallbackErr := fallback.Synthesize(ctx, text)
		if fallbackErr != nil {
			return nil, true, observation, fmt.Errorf("kokoro fallback (sticky): %w", fallbackErr)
		}
		return fallbackStream, true, observation, nil
	}
	primaryObservation := p.observeProvider(primary)
	primaryObservation.synthStarted = time.Now()
	stream, err := primary.Synthesize(ctx, text)
	if err == nil {
		p.boundPrimarySilence(stream, len(text))
		return stream, false, primaryObservation, nil
	}
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(err) {
		primaryObservation.exclusions = []string{fmt.Sprintf("primary synthesis failed: %v", err)}
		return nil, false, primaryObservation, err
	}
	p.emit(events.Error{Stage: events.StageTTSFallback, Message: ttsFallbackMessage("primary TTS failed", err, stickFallback != nil)})
	fallbackObservation := p.observeProvider(fallback)
	fallbackObservation.synthStarted = time.Now()
	fallbackObservation.exclusions = []string{fmt.Sprintf("primary synthesis failed: %v", err)}
	fallbackStream, fallbackErr := fallback.Synthesize(ctx, text)
	if fallbackErr != nil {
		return nil, true, fallbackObservation, fmt.Errorf("primary TTS: %v; Kokoro fallback: %w", err, fallbackErr)
	}
	if stickFallback != nil {
		*stickFallback = true
	}
	return fallbackStream, true, fallbackObservation, nil
}

func (p *Pipeline) observeProvider(provider tts.Provider) synthesisObservation {
	return synthesisObservation{
		engine:        ttsIdentityFor(provider),
		warmState:     ttsWarmStateOf(provider),
		wholeSentence: ttsWholeSentenceOf(provider),
	}
}

func ttsWholeSentenceOf(provider tts.Provider) bool {
	d, ok := provider.(tts.AdviceCapability)
	return ok && d.WholeSentencePlayback()
}

// ttsFallbackMessage describes a Kokoro fallback switch. When sticky is true
// the turn will stay on fallback for later sentences; otherwise only this
// utterance is affected (speakBestEffort / text-mode paths).
func ttsFallbackMessage(prefix string, err error, sticky bool) string {
	if sticky {
		return fmt.Sprintf("%s; speaking with Kokoro (different voice) for the rest of the reply: %v", prefix, err)
	}
	return fmt.Sprintf("%s; retrying this sentence with Kokoro (different voice): %v", prefix, err)
}

// playFallback retries an error returned after Player takes ownership of the
// primary stream. This covers file-oriented native workers whose failure is
// reported when the stream becomes ready.
func (p *Pipeline) playFallback(ctx context.Context, text string, primaryErr error, stickFallback *bool) (*audio.Playback, bool, error) {
	playback, fallback, _, err := p.playFallbackObserved(ctx, text, primaryErr, stickFallback)
	return playback, fallback, err
}

func (p *Pipeline) playFallbackObserved(ctx context.Context, text string, primaryErr error, stickFallback *bool) (*audio.Playback, bool, synthesisObservation, error) {
	primary, fallback := p.responseTTS(ctx)
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(primaryErr) {
		return nil, false, p.observeProvider(primary), primaryErr
	}
	p.emit(events.Error{Stage: events.StageTTSFallback, Message: ttsFallbackMessage("primary playback TTS failed", primaryErr, stickFallback != nil)})
	fallbackObservation := p.observeProvider(fallback)
	fallbackObservation.synthStarted = time.Now()
	fallbackObservation.exclusions = []string{fmt.Sprintf("primary playback failed: %v", primaryErr)}
	stream, err := fallback.Synthesize(ctx, text)
	if err != nil {
		return nil, true, fallbackObservation, fmt.Errorf("primary playback TTS: %v; Kokoro fallback: %w", primaryErr, err)
	}
	fallbackObservation.stream = stream
	if stickFallback != nil {
		*stickFallback = true
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	return playback, true, fallbackObservation, err
}

// shouldFallback limits Kokoro recovery to provider failures that may be
// transient or caused by an unusable worker output. Permanent request/config
// errors must reach the user instead of being silently converted into a
// different voice policy.
func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	// A stalled engine is precisely what the fallback voice exists for.
	if errors.Is(err, audio.ErrStreamStalled) {
		return true
	}
	// An explicit provider classification outranks the raw cause. A worker that
	// times out reports ProviderErrorWorker while still unwrapping to
	// context.DeadlineExceeded, so sniffing the cause first refused the
	// fallback for the exact failure it should have covered. Turn-level
	// cancellation is still refused below, and canUseFallback re-checks the
	// turn context before anything is synthesized.
	if tts.IsProviderErrorKind(err, tts.ProviderErrorCanceled) {
		return false
	}
	if tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorWorker) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorMalformed) {
		return true
	}
	return false
}

// boundPrimarySilence arms the primary engine's stall budget on a synthesis
// stream so a worker that produces no audio fails the segment over to the
// fallback voice instead of holding the turn until the watchdog abandons it.
//
// Only the primary is bounded. The fallback is the last voice available, so
// failing it would produce silence rather than another retry. Providers that
// publish no grace (Kokoro) leave the stream unbounded, exactly as before.
func (p *Pipeline) boundPrimarySilence(stream *audio.PCMStream, chars int) {
	if stream == nil {
		return
	}
	if budget := p.ttsFirstAudioGrace(chars); budget > 0 {
		stream.SetStallBudget(budget)
	}
}

func (p *Pipeline) canUseFallback(ctx context.Context, primary, fallback tts.Provider) bool {
	return ctx.Err() == nil && !p.OutputMuted() && fallback != nil && fallback != primary && fallback.Available()
}
