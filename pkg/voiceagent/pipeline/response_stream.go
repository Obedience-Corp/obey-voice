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
	streamedChunks, tokenTick, observeDone := p.observeStream(streamCtx, stream, metrics)
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
	captureOwner := completionCaptureFromContext(ctx)

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
			// An outer failure can win the select against a qualified request.
			// Preserve that input even when this response is already ending.
			if !interrupted {
				select {
				case req := <-bargeIn:
					p.retainInterruptedCapture(req, metrics.sincePlaybackStart(req.At))
					if captureOwner != nil {
						captureOwner.retain = true
					}
				default:
				}
			}
			// Failure/cancellation uses the same pause-speech retention as
			// a normal finish; it does not itself qualify microphone input.
			if !interrupted && ctrl.sawPauseSpeechWithin(bargeInNearMissWindow) {
				p.keepCapture.Store(true)
				if captureOwner != nil {
					captureOwner.retain = true
				}
			}
		}()
	}

	interrupt := func(req interruptRequest) error {
		if interrupted {
			return nil
		}
		interrupted = true
		metrics.interrupted = true
		p.retainInterruptedCapture(req, metrics.sincePlaybackStart(req.At))
		metrics.markBargeIn(req.At)
		turn.to(TurnInterrupted)
		if metrics.delivery != nil {
			if c := p.WorkCoordinator(); c != nil {
				_ = c.RevokeCompletionLease(metrics.delivery.lease)
			}
		}
		p.softCancelTTS("barge-in")
		cancel()
		if p.Player != nil {
			p.Player.Stop()
		}
		stoppedAfter := time.Since(req.At)
		// Stop the speaker before waiting for the capture-owner boundary.
		// The qualified request still authorizes retention after revoking the
		// output fence cancels this response. Keep cleanup scoped to the original
		// capture token and bounded independently of that cancellation.
		captureErr := captureOwner.confirm(context.WithoutCancel(ctx))
		if captureErr != nil {
			p.emit(events.Error{Stage: "capture", Message: captureErr.Error()})
		}
		if cancelTurn != nil {
			cancelTurn()
		}
		sincePlayback := metrics.sincePlaybackStart(req.At)
		p.emitResponse(metrics, events.SpeakingInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback, DetectionToPlaybackStop: stoppedAfter})
		p.emitResponse(metrics, events.TurnInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback})
		return captureErr
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
	// provider mutex for the duration (see the grouping policy below). One worker
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

	// Segment grouping: one thought is one synthesis call.
	//
	// The opening stays short because time-to-first-audio waits on it finishing
	// synthesis — but an interjection ("Wait.") is not an opening, it is the
	// front half of one, and sending it alone buys the listener two prosody
	// samples where they expect one. After the opening the listener is no
	// longer waiting, so groups fill to laterFloorChars — lowered to the
	// provider's ramp, bounded by the ceiling (see speech_content.go).
	//
	// This lives here rather than in brain.ChunkSentencesRaw because only this
	// loop knows which segments become audio — the voice gate below suppresses
	// tool output and code fences, and cleaning can empty a segment. Counting
	// emitted chunks instead of audible ones spent the opening's budget on
	// silence whenever a reply began with a suppressed tool line. The provider
	// is only reachable here too.
	primaryVoice, fallbackVoice := p.responseTTS(ctx)
	grouping := groupingFor(primaryVoice)
	// Grouping follows the engine that speaks. Once a segment falls back, the
	// rest of the turn is synthesized by the fallback, so its join mode and
	// ramp decide the remaining flushes — holding a "?" beat for a request
	// join Kokoro re-splits anyway is the defect this avoids.
	var spokeFallback bool
	var voiceBatch []string
	var batchTextReady time.Time
	var batchBreak bool
	lastGroupChars := 0
	// idle fires when a complete sentence has been buffered and the brain has
	// gone quiet: a tool call or a long pause must not hold speech hostage.
	// Stopped until a segment is buffered; Stop/Reset never leave a stale value
	// on the channel (Go 1.23 timers), so the loop's case cannot fire unarmed.
	idleAfter := p.idleFlushDelay()
	idle := time.NewTimer(idleAfter)
	idle.Stop()
	defer idle.Stop()
	armIdle := func() { idle.Reset(idleAfter) }
	stopIdle := func() { idle.Stop() }
	flushVoiceBatch := func() {
		if len(voiceBatch) == 0 {
			return
		}
		text := strings.Join(voiceBatch, " ")
		// The loop, not the text, decides whether this group may be rewritten
		// into one contour: it is the only place that knows the group was
		// assembled by holding a short beat for the sentence it belongs to.
		joinBeats := grouping.textualJoin && joinableBeatGroup(voiceBatch)
		if !watchdogArmed {
			watchdogArmed = true
			// Sized from the flushed group, not the first admitted segment:
			// a merged opening is longer than the beat that started it and
			// would otherwise run under a one-sentence grace. Arm before the
			// handoff so a hung WaitReady is visible.
			stallTimeout := p.stallTimeout(len(text))
			metrics.firstAudioGrace = stallTimeout
			metrics.ttsEngine = p.ttsIdentity()
			metrics.sinkAtArm = p.sinkKind()
			metrics.audibleSink = metrics.sinkAtArm
			p.warnNoSinkSynth(metrics.sinkAtArm)
			go p.watchPlaybackStall(streamCtx, &audioStarted, cancel, stalled, stallTimeout, len(text), metrics.sinkAtArm == audio.SinkNone)
		}
		pending++
		if metrics.delivery != nil {
			metrics.delivery.queued++
		}
		queuedAt := time.Now()
		synthQueue <- synthWork{sentence: text, textReady: batchTextReady, queuedAt: queuedAt, joinBeats: joinBeats}
		voiceBatch = voiceBatch[:0]
		batchTextReady = time.Time{}
		batchBreak = false
		// The target this group was measured against, kept beside the segment
		// so a short boundary is read against the policy that flushed it
		// rather than a floor the ramp never allowed it to reach.
		metrics.noteFlushTarget(grouping.flushTarget(lastGroupChars))
		lastGroupChars = speakableChars(text)
		stopIdle()
	}
	// routeSegment sends one think-filtered segment to the transcript and, if it
	// survives gating and cleaning, to the voice batch. The end-of-stream think
	// flush enters here too, so both entry points share one copy of the
	// transcript and speakability rules instead of drifting apart.
	// paragraphStart marks a segment the chunker emitted behind a blank line;
	// it ends the held group before this sentence joins it.
	routeSegment := func(text string, paragraphStart bool) {
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
		// A blank line before this sentence, or a group that would outgrow the
		// ceiling, ends the held group before this sentence joins it. Both
		// yield to a held tag-only tail, which has no prose of its own and has
		// to ride into this sentence — the rule ready() already applies.
		if (paragraphStart && !heldTagTail(voiceBatch)) || exceedsCeiling(voiceBatch, speakable) {
			flushVoiceBatch()
		}
		voiceBatch = append(voiceBatch, speakable)
		if batchTextReady.IsZero() {
			batchTextReady = now
		}
		// A blank line inside the segment ends the group after it. Read from
		// the raw segment: cleaning runs TrimSpace and does not preserve it.
		if strings.Contains(text, "\n\n") {
			batchBreak = true
		}
		// At most one handoff per segment: the intake gate guarantees a single
		// free queue slot, and a blocking send inside the loop wedges the
		// turn. A group that cannot go now goes on the idle timer.
		if grouping.ready(voiceBatch, batchBreak, lastGroupChars, false) && pending < voiceQueueDepth {
			flushVoiceBatch()
			return
		}
		armIdle()
	}

	// drainToken takes a pending tick without blocking. A tick delivered in the
	// same round as the idle timer means the brain is still producing, so the
	// countdown restarts instead of the group going out into its own follower.
	drainToken := func() bool {
		select {
		case _, ok := <-tokenTick:
			if !ok {
				tokenTick = nil
				return false
			}
			return true
		default:
			return false
		}
	}

	// takeSentence applies one segment from the segmenter, end-of-stream close
	// included. The idle case calls it too: a follower that arrived in the same
	// round as the timer must win, or the beat the timer was holding is spoken
	// without it.
	takeSentence := func(sentence string, ok bool) {
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
			routeSegment(proj.flushThink(), false)
			// A half-full batch is still a real segment: flush it before
			// closing, or the last sentence of every odd-length reply is
			// silently never spoken.
			if !interrupted {
				flushVoiceBatch()
			}
			closeSynthQueue() // no more handoffs; let the worker exit
			return
		}

		paragraphStart := strings.HasPrefix(sentence, brain.ParagraphMark)
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			return
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
		routeSegment(proj.filterThink(sentence), paragraphStart)
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
			if err := interrupt(req); err != nil {
				return strings.TrimSpace(fullResponse.String()), interrupted, err
			}

		case _, ok := <-tokenTick:
			// A token means the sentence after the buffered one is still
			// coming. Restart the countdown rather than flush into it. Niled
			// on close: a closed channel would deliver forever and spin.
			if !ok {
				tokenTick = nil
				continue
			}
			if len(voiceBatch) > 0 {
				armIdle()
			} else {
				stopIdle()
			}

		case <-idle.C:
			// Only complete sentences are ever buffered — the segmenter holds
			// a partial until its punctuation arrives — so a non-empty batch
			// is safe to speak. The pending gate is load-bearing: the handoff
			// below blocks on synthQueue, and blocking inside this select
			// wedges the turn.
			if len(voiceBatch) == 0 || interrupted {
				continue
			}
			// select gives no priority, so the timer can win a round against
			// the token or the follower the group is waiting for. Take those
			// first; only a brain that has actually gone quiet flushes here.
			if drainToken() {
				armIdle()
				continue
			}
			select {
			case sentence, ok := <-sentenceCh:
				takeSentence(sentence, ok)
				// The take can route to nothing — a duplicate, a gated line, a
				// muted turn — and then nothing re-arms the timer this round
				// consumed, so the held group waits for the next token or end
				// of stream instead of the deadline it had already reached.
				if len(voiceBatch) > 0 {
					armIdle()
				}
				continue
			default:
			}
			if pending >= voiceQueueDepth {
				armIdle()
				continue
			}
			// D4: a group that waited out the brain goes even under target, so
			// a tool call or a think pause cannot hold speech hostage. The one
			// thing the timer does not force is a tag-only tail, which has no
			// prose of its own and rides with the next sentence.
			if !grouping.ready(voiceBatch, batchBreak, lastGroupChars, true) {
				armIdle()
				continue
			}
			flushVoiceBatch()

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
				metrics.route.Tier, metrics.route.ReasonCode = result.RouteTier, result.RouteReasonCode
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
			takeSentence(sentence, ok)

		case event := <-playbackEvents:
			if event.fallback && !spokeFallback && fallbackVoice != nil {
				spokeFallback = true
				grouping = groupingFor(fallbackVoice)
			}
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
		// A qualified request can race the final playback event. Joining the
		// watcher closes that race before the completion can publish delivery.
		select {
		case req := <-bargeIn:
			if err := interrupt(req); err != nil {
				return strings.TrimSpace(fullResponse.String()), interrupted, err
			}
		default:
		}
		// Near-miss retention (F3.3): the watcher heard pause speech that never
		// reached the trip threshold. The utterance is in the capture ring
		// buffer — keep it so the next listen transcribes it instead of
		// Reset()ing it away.
		if !interrupted && ctrl != nil && ctrl.sawPauseSpeechWithin(bargeInNearMissWindow) {
			p.keepCapture.Store(true)
			if captureOwner != nil {
				captureOwner.retain = true
			}
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
//
// The second channel ticks once per reply token. The turn loop's idle flush
// needs to know the brain is still producing, and it cannot see that from
// stream.Chunks: this stage is the sole consumer, and what reaches the loop is
// whole sentences. The send is non-blocking into a cap-1 channel, so a loop
// busy elsewhere loses ticks instead of stalling the observer. Thinking tokens
// deliberately do not tick — a reply already segmented has nothing to wait for
// while the model reasons.
func (p *Pipeline) observeStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (<-chan string, <-chan struct{}, <-chan struct{}) {
	out := make(chan string, 8)
	ticks := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(ticks)
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
				select {
				case ticks <- struct{}{}:
				default:
				}
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

	return out, ticks, done
}

// collectTextStream drains a brain stream to completion for the text-input
// path, letting observeStream emit ResponseStreamingStarted and per-chunk
// ResponseDelta events along the way, and returns the full accumulated text.
// It waits on stream.Done for the terminal error so a failed stream surfaces
// even after partial chunks were displayed.
func (p *Pipeline) collectTextStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (string, string, error) {
	out, _, done := p.observeStream(ctx, stream, metrics)

	var b strings.Builder
	for chunk := range out {
		b.WriteString(chunk)
	}
	<-done

	select {
	case res := <-stream.Done:
		metrics.route.Tier, metrics.route.ReasonCode = res.RouteTier, res.RouteReasonCode
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
