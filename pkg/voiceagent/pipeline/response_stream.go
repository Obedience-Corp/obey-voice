package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func (p *Pipeline) streamResponse(ctx context.Context, cancelTurn context.CancelFunc, stream *brain.Stream, allowBargeIn bool, metrics *turnMetrics, turn *turnConductor, repair *promiseRepairState) (string, bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	// loopDone closes when this function returns — the moment the loop stops
	// draining playbackEvents. Playback watchers select on it so their sends
	// cannot block (and leak) after an early return (ctx cancel, stall, or brain
	// error). It is distinct from streamCtx on purpose: a barge-in cancels
	// streamCtx but the loop keeps reading playbackEvents to drain pending
	// playbacks, so watchers must still deliver their finished events.
	loopDone := make(chan struct{})
	defer close(loopDone)

	// Two narrow upstream stages feed the turn loop: the model observer forwards
	// brain chunks (stamping first-chunk metrics), then the segmenter turns that
	// chunk stream into voice-ready sentences. Both are testable in isolation.
	streamedChunks, observeDone := p.observeStream(streamCtx, stream, metrics)
	// Join the observer before returning: it stamps firstModelChunk, which the
	// caller reads via snapshot() in finish(). Cancel first so the goroutine
	// unblocks, then wait for it to exit. cancel is idempotent — the barge-in and
	// normal-completion paths also call it. This runs after close(loopDone).
	defer func() {
		cancel()
		<-observeDone
	}()
	// Raw segments: the voice gate needs the markdown fences CleanForVoice
	// strips, so gating happens first and each destination cleans after.
	sentences := brain.ChunkSentencesRaw(streamedChunks)
	// One projection per turn holds the think filter, the voice gate and the
	// spoken-line memory, so the rules a segment is judged by live in one place
	// and a measurement harness can ask for them instead of re-deriving them.
	proj := NewSpeechProjection()

	var fullResponse strings.Builder
	// heard drops a sentence this turn already put on the transcript; the
	// projection drops one already in the synth queue. Local models often emit
	// the spoken line, fire a phantom tool, then emit the same line again
	// (Veronica, 2026-08-28: web_search for the World Cup mid-conversation,
	// then the identical sentence a second time). Without this she says it
	// twice.
	heard := make(map[string]struct{})
	var interrupted bool
	var pending int
	var bargeIn <-chan interruptRequest
	var bargeDone <-chan struct{}
	// armAt zero = unarmed: barge-in stays off until the turn's first playback
	// arms it, once, in applyPlaybackEvent. Speech during the thinking phase is
	// a separate product decision (F3.4) and stays disarmed here.
	var armAt atomic.Int64
	var ctrl *interruptController

	if allowBargeIn {
		ctrl = p.newInterruptController()
		watch := ctrl.watchWithDone(streamCtx, &armAt)
		bargeIn = watch.requests
		bargeDone = watch.done
		// Join the watcher on every exit path — including early returns via
		// outer cancellation, playback stall, and brain error — so its capture
		// subscription and VAD Clear never overlap the next turn. Cancel first
		// so it unblocks; joining twice is fine (done is closed).
		defer func() {
			if bargeDone == nil {
				return
			}
			cancel()
			<-bargeDone
		}()
	}

	playbackEvents := make(chan playbackEvent, voiceQueueDepth*2)
	modelDone := stream.Done
	modelFinished := false

	var audioStarted atomic.Bool
	var realAudioQueued atomic.Bool
	stalled := make(chan struct{})
	watchdogArmed := false

	// Progressive sentence TTS: brain.ChunkSentences emits each complete
	// sentence as stream tokens arrive; we hand each segment to the warm TTS
	// worker (native qwen3-tts-worker or Kokoro) without waiting for the full
	// brain reply. That is the product progressive path for long replies.
	//
	// Synthesis runs on a single ordered worker so this loop keeps servicing
	// barge-in and playback events while a sentence is being generated —
	// PlayStream cannot return until the TTS engine has produced audio, and
	// Kokoro generates a whole BATCH per uncancellable cgo call, holding its
	// provider mutex for the duration (see openingBatchSegments). One worker
	// (not one goroutine per sentence) preserves playback order. Backpressure is
	// unchanged: the pending gate keeps at most voiceQueueDepth sentences
	// outstanding, so the buffered handoff below never blocks.
	//
	// stickFallback is turn-scoped: once any sentence falls back to Kokoro,
	// remaining sentences stay on Kokoro so a flaky Qwen worker cannot flip
	// mid-reply between persona voice and the fallback voice.
	var stickFallback bool
	synthQueue := make(chan synthWork, voiceQueueDepth)
	var synthQueueOnce sync.Once
	closeSynthQueue := func() { synthQueueOnce.Do(func() { close(synthQueue) }) }
	defer closeSynthQueue()

	// Adaptive batching: the first segment that will actually be heard goes out
	// alone, and later segments are synthesized two at a time.
	//
	// The opening stays short because time-to-first-audio waits on it finishing
	// synthesis. After that the listener is no longer waiting, and a chunk
	// boundary is not free: each batch is an independent synthesis call, so
	// prosody resets at every boundary and the player concatenates raw buffers.
	// Batching lets the synthesiser carry intonation across a sentence boundary.
	//
	// This lives here rather than in brain.ChunkSentencesRaw because only this
	// loop knows which segments become audio — the voice gate below suppresses
	// tool output and code fences, and cleaning can empty a segment. Counting
	// emitted chunks instead of audible ones spent the opening's budget on
	// silence whenever a reply began with a suppressed tool line.
	var voiceBatch []string
	var batchTextReady time.Time
	openingSpoken := false
	flushVoiceBatch := func() {
		if len(voiceBatch) == 0 {
			return
		}
		pending++
		if metrics.delivery != nil {
			metrics.delivery.queued++
		}
		queuedAt := time.Now()
		synthQueue <- synthWork{sentence: strings.Join(voiceBatch, " "), textReady: batchTextReady, queuedAt: queuedAt}
		voiceBatch = voiceBatch[:0]
		batchTextReady = time.Time{}
		openingSpoken = true
	}
	// routeSegment sends one think-filtered segment to the transcript and, if it
	// survives gating and cleaning, to the voice batch. The end-of-stream think
	// flush enters here too, so both entry points share one copy of the
	// transcript and speakability rules instead of drifting apart.
	routeSegment := func(text string) {
		if text == "" {
			return
		}

		// Feed the gate every segment, in order, before deciding whether
		// this turn speaks at all: its regions span segments, so skipping
		// one loses the boundary that closes a tool block or a fence.
		voiceText, stripped := proj.gateSegment(text)

		// Filler-only chunks are provisional: providers replace a turn that
		// ends there with the recovery reply. Keep them out of both transcript
		// and TTS so the finalizer's replacement is the only visible/audible
		// response. A filler followed by content in the same sentence survives.
		if display := stripVocalTags(brain.CleanForVoice(text)); speakableSegment(display) {
			if _, dup := heard[display]; !dup {
				heard[display] = struct{}{}
				if fullResponse.Len() > 0 {
					fullResponse.WriteByte(' ')
				}
				fullResponse.WriteString(display)
			}
		}

		if repair != nil && repair.shouldRepair(fullResponse.String()) {
			if p.Player != nil {
				p.Player.Stop()
			}
			return
		}

		if interrupted || p.OutputMuted() || p.Player == nil || !p.ttsReady() {
			if metrics.delivery != nil && strings.TrimSpace(text) != "" {
				metrics.delivery.skipped = true
			}
			return
		}

		p.recordStrips(stripped, metrics)
		// Cleaning, the speakability predicate and this turn's repeat
		// suppression, all of which the bench harness replays through the
		// same code (SpeechProjection).
		speakable, admission := proj.admit(voiceText)
		if admission != speechAdmitted {
			// Duplicate prose is already represented by an earlier queued
			// segment. Its actual playback receipt still governs delivery.
			if metrics.delivery != nil && admission != speechAlreadyAdmitted && strings.TrimSpace(text) != "" {
				metrics.delivery.skipped = true
			}
			return
		}

		turn.to(TurnSpeaking)
		now := time.Now()
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = now
		}
		if metrics.firstAdmit.IsZero() {
			metrics.firstAdmit = now
		}
		if !watchdogArmed {
			watchdogArmed = true
			// The budget is sized to the segment the listener is actually
			// waiting on. openingBatchSegments is 1, so this speakable is
			// that segment. Arm before PlayStream so hung WaitReady is visible.
			stallTimeout := p.stallTimeout(len(speakable))
			metrics.firstAudioGrace = stallTimeout
			metrics.ttsEngine = p.ttsIdentity()
			metrics.sinkAtArm = p.sinkKind()
			metrics.audibleSink = metrics.sinkAtArm
			p.warnNoSinkSynth(metrics.sinkAtArm)
			go p.watchPlaybackStall(streamCtx, &audioStarted, cancel, stalled, stallTimeout, len(speakable), metrics.sinkAtArm == audio.SinkNone)
		}

		voiceBatch = append(voiceBatch, speakable)
		if batchTextReady.IsZero() {
			batchTextReady = now
		}
		target := laterBatchSegments
		if !openingSpoken {
			target = openingBatchSegments
		}
		if len(voiceBatch) >= target {
			flushVoiceBatch()
		}
	}
	go func() {
		// Segments are numbered in playback order by the single ordered synth
		// worker, so the index is the position a listener hears.
		index := 0
		for work := range synthQueue {
			outcome := p.synthesizeSegmentObserved(streamCtx, loopDone, work, index, &audioStarted, playbackEvents, &stickFallback, &realAudioQueued)
			if !outcome.enqueued {
				// No playback was enqueued: release the pending slot so the
				// loop's accounting and the intake gate stay correct.
				if outcome.failure != nil {
					sendPlaybackEvent(loopDone, playbackEvents, *outcome.failure)
				} else {
					sendPlaybackEvent(loopDone, playbackEvents, playbackEvent{kind: playbackNotEnqueued, sentence: work.sentence, index: index})
				}
			}
			index++
		}
	}()

	for sentences != nil || pending > 0 || modelDone != nil {
		// Pause sentence intake while the playback queue is full so the loop
		// always returns to select and can drain playbackEvents. Blocking here
		// to acquire a slot would deadlock the turn once the queue fills.
		var sentenceCh <-chan string
		if pending < voiceQueueDepth {
			sentenceCh = sentences
		}

		select {
		case <-ctx.Done():
			if interrupted {
				return strings.TrimSpace(fullResponse.String()), interrupted, nil
			}
			return strings.TrimSpace(fullResponse.String()), interrupted, ctx.Err()

		case <-stalled:
			if metrics.sinkAtArm == audio.SinkNone {
				return strings.TrimSpace(fullResponse.String()), interrupted, nil
			}
			metrics.stallWatchdogFired = true
			return strings.TrimSpace(fullResponse.String()), interrupted, errPlaybackStalled

		case req := <-bargeIn:
			if interrupted {
				continue
			}
			interrupted = true
			if metrics.delivery != nil {
				if c := p.WorkCoordinator(); c != nil {
					_ = c.RevokeCompletionLease(metrics.delivery.lease)
				}
			}
			metrics.interrupted = true
			metrics.markBargeIn(req.At)
			turn.to(TurnInterrupted)
			// Send the session's actual active request ID without waiting on the
			// synthesis lock. Stage B workers can stop mid-synthesis; Stage A
			// exits through the canceled context and is restarted by the provider.
			p.softCancelTTS("barge-in")
			cancel()
			if cancelTurn != nil {
				cancelTurn()
			}
			if p.Player != nil {
				p.Player.Stop()
			}
			sincePlayback := metrics.sincePlaybackStart(req.At)
			p.emitResponse(metrics, events.SpeakingInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback})
			p.emitResponse(metrics, events.TurnInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback})

		case result, ok := <-modelDone:
			if !ok {
				modelDone = nil
				continue
			}
			modelDone = nil
			modelFinished = true
			metrics.modelComplete = time.Now()
			p.emitResponse(metrics, events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
			if repair != nil && result.Route != "" {
				repair.applyBrainRoute(result.Route)
				if _, _, ok := repair.routedKind(); ok && p.Player != nil {
					p.Player.Stop()
				}
			}
			if result.Err != nil && !interrupted {
				if result.Recovered {
					// The brain already streamed a spoken recovery reply
					// through Chunks; let the turn drain and complete degraded
					// while the failure lands in the activity feed.
					metrics.degraded = true
					p.emit(events.Error{Stage: "brain", Message: result.Err.Error()})
					logDegradedTurn("brain", result.Err)
					continue
				}
				if p.Player != nil {
					p.Player.Stop()
				}
				return strings.TrimSpace(fullResponse.String()), interrupted, fmt.Errorf("brain: %w", mapIdleDeadline(ctx, result.Err))
			}

		case sentence, ok := <-sentenceCh:
			if !ok {
				sentences = nil
				// Drain the think filter: Feed holds a trailing "<" in case it
				// opens a think tag, and at end of stream nothing more can
				// arrive to disambiguate it — the same end-of-stream Flush the
				// label stripper does in brain.go.
				//
				// This cannot change what is spoken today: the hold is only ever
				// a prefix of a tag, so the one value Flush releases is a lone
				// "<", which speakableSegment rejects anyway. It routes rather
				// than drops so that if the hold ever carries content, the loop
				// judges it by the same predicate as every other segment.
				routeSegment(proj.flushThink())
				// A half-full batch is still a real segment: flush it before
				// closing, or the last sentence of every odd-length reply is
				// silently never spoken.
				if !interrupted {
					flushVoiceBatch()
				}
				closeSynthQueue() // no more handoffs; let the worker exit
				continue
			}

			sentence = strings.TrimSpace(sentence)
			if sentence == "" {
				continue
			}

			// Think blocks come off before anything else reads the segment.
			// They look like speech (letters, periods) so the gate would read
			// them aloud, and the same filtered text feeds the transcript — so
			// the model never reads its own reasoning back out of conversation
			// history on the next turn, paying its tokens every turn after.
			// A tag still on Chunks after the brain split is a leak: strip it
			// and tell the operator, but do not stall the turn.
			if textclean.ContainsThinkTag(sentence) {
				metrics.thinkLeak++
				p.emit(events.Error{Stage: "think", Message: "think markup leaked onto the speech bus"})
			}
			routeSegment(proj.filterThink(sentence))

		case event := <-playbackEvents:
			if interrupted && event.kind == playbackStarted && p.Player != nil {
				// A synthesis that raced the barge-in finished after Stop();
				// stop the late playback immediately.
				p.Player.Stop()
			}
			if p.applyPlaybackEvent(event, metrics, &armAt) {
				pending--
			}
		}
	}

	if !modelFinished {
		metrics.modelComplete = time.Now()
		p.emitResponse(metrics, events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
	}

	if !interrupted {
		cancel()
		if bargeDone != nil {
			<-bargeDone
		}
		// Near-miss retention (F3.3): the watcher heard pause speech that never
		// reached the trip threshold. The utterance is in the capture ring
		// buffer — keep it so the next listen transcribes it instead of
		// Reset()ing it away.
		if ctrl != nil && ctrl.sawPauseSpeechWithin(bargeInNearMissWindow) {
			p.keepCapture.Store(true)
		}
		p.resetEchoState()
	}

	return strings.TrimSpace(fullResponse.String()), interrupted, nil
}

// observeStream is the model-observer stage. It is the sole consumer of
// stream.Chunks and stream.Thinking. Chunks are forwarded for sentence
// segmentation; Thinking is drained so a thinking producer cannot block, and
// emitted as ThinkingDelta for the operator. The terminal result path
// (stream.Done) is owned separately by streamResponse.
//
// A closed Thinking channel must be niled: a closed case delivers "" forever
// and can starve Chunks.
//
// The forwarding send is cancellation-safe: on ctx cancellation the stage
// returns instead of blocking on a downstream that has stopped reading.
// The returned done channel closes once the goroutine has fully exited, so a
// caller can join it before reading metrics (it stamps firstModelChunk) and
// avoid racing snapshot() on an early return.
func (p *Pipeline) observeStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (<-chan string, <-chan struct{}) {
	out := make(chan string, 8)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(out)
		chunks, thinking := stream.Chunks, stream.Thinking
		stampToken := func(now time.Time) {
			if metrics.firstModelChunk.IsZero() {
				metrics.firstModelChunk = now
			}
			if metrics.firstToken.IsZero() {
				metrics.firstToken = now
				p.emitResponse(metrics, events.ResponseStreamingStarted{Elapsed: metrics.elapsedFromEOU(now)})
			}
		}
		for chunks != nil || thinking != nil {
			select {
			case <-ctx.Done():
				return
			case t, ok := <-thinking:
				if !ok {
					thinking = nil
					continue
				}
				if t == "" {
					continue
				}
				p.bumpBrainDeadline()
				now := time.Now()
				stampToken(now)
				if metrics.firstThink.IsZero() {
					metrics.firstThink = now
				}
				p.emitResponse(metrics, events.ThinkingDelta{Text: t})
			case c, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if c == "" {
					continue
				}
				p.bumpBrainDeadline()
				now := time.Now()
				stampToken(now)
				if metrics.firstSpeakable.IsZero() && strings.TrimSpace(c) != "" {
					metrics.firstSpeakable = now
					if !metrics.firstSpeakable.Equal(metrics.firstToken) {
						p.emitResponse(metrics, events.ResponseSpeakableStarted{Elapsed: metrics.elapsedFromEOU(now)})
					}
				}
				p.emitResponse(metrics, events.ResponseDelta{Text: c})
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
			}
		}
	}()

	return out, done
}

// collectTextStream drains a brain stream to completion for the text-input
// path, letting observeStream emit ResponseStreamingStarted and per-chunk
// ResponseDelta events along the way, and returns the full accumulated text.
// It waits on stream.Done for the terminal error so a failed stream surfaces
// even after partial chunks were displayed.
func (p *Pipeline) collectTextStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (string, string, error) {
	out, done := p.observeStream(ctx, stream, metrics)

	var b strings.Builder
	for chunk := range out {
		b.WriteString(chunk)
	}
	<-done

	select {
	case res := <-stream.Done:
		if res.Err != nil {
			if res.Recovered {
				// The streamed text already ends with the brain's recovery
				// reply; keep it and surface the failure as activity detail.
				metrics.degraded = true
				p.emit(events.Error{Stage: "brain", Message: res.Err.Error()})
				logDegradedTurn("brain", res.Err)
				return strings.TrimSpace(b.String()), res.Route, nil
			}
			// Return any partial text so the caller's recovery path can keep
			// it in the transcript alongside the recovery reply.
			return strings.TrimSpace(b.String()), res.Route, mapIdleDeadline(ctx, res.Err)
		}
		return strings.TrimSpace(b.String()), res.Route, nil
	case <-ctx.Done():
		return strings.TrimSpace(b.String()), "", mapIdleDeadline(ctx, ctx.Err())
	}
}
