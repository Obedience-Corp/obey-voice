package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// completeTextTurn finishes a successful text-mode turn exactly once:
// ResponseReady for the transcript, terminal metrics via finish, and OnTurn
// for session auto-save. Every degraded and mute short-circuit must use this
// so finish logic cannot drift across early returns.
func (p *Pipeline) completeTextTurn(turn *turnConductor, metrics *turnMetrics, response string) {
	p.finishOrdinaryCompletion(metrics, response, false)
	p.emitResponse(metrics, events.ResponseReady{Response: response, Degraded: metrics.degraded})
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	if err := p.ValidateImages(ctx, chatmedia.Input(ctx)); err != nil {
		return err
	}
	p.incActiveTurn()
	defer p.decActiveTurn()
	return p.runTextWithContinuations(ctx, input)
}

func (p *Pipeline) runTextTurn(ctx context.Context, input string, background bool) error {
	p.incActiveTurn()
	defer p.decActiveTurn()
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	if !background {
		// Typed input is final when admitted to this host turn, before persona
		// preparation or compaction. Include that work in user-visible latency.
		metrics.sttFinal = metrics.start
	}
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics, ctx)
	ctx = events.WithTrace(ctx, metrics.trace)

	turn.to(TurnThinking)
	continuation, isContinuation := continuationFromContext(ctx)
	speakerID := p.currentSpeakerID()
	if isContinuation {
		input = continuation.admission.Request
		speakerID = ""
	} else if background {
		speakerID = ""
	} else {
		p.emitResponse(metrics, events.UserInput{Text: input, Speaker: speakerID})
	}
	original := input
	var pendingDecision *decisionTurn
	if !p.GroupParticipationEnabled && len(chatmedia.Input(ctx)) == 0 {
		pendingDecision = p.startWorkDecision(ctx, input, decision.TextWork, background || isContinuation)
	}
	defer func() { pendingDecision.close() }()
	var admission ResponseAdmission
	if isContinuation {
		admission = continuation.admission
		ctx = p.bindAdmission(ctx, "", admission, metrics)
	} else {
		var err error
		ctx, admission, err = p.admitInput(ctx, input, background, metrics)
		if err != nil {
			turn.finish(TurnFailed)
			return wrapAdmissionError(err)
		}
	}
	if admission.Skip {
		turn.finish(TurnYielded)
		return nil
	}
	if admission.Discussion != nil {
		var stop func()
		ctx, stop = discussion.Bind(ctx, admission.Discussion.State)
		defer stop()
	}
	if p.GroupParticipationEnabled && admission.Discussion == nil {
		pendingDecision = p.startWorkDecision(ctx, input, decision.TextWork, background || isContinuation)
	}
	if !isContinuation && admission.LocalReply != "" {
		return p.completeLocalReply(ctx, original, admission.LocalReply, metrics, turn)
	}
	input = admission.Request
	if err := p.prepareTurn(ctx, input, metrics, turn); err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return err
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("compact: %w", err))
		return nil
	}
	p.emitResponse(metrics, events.ThinkingStarted{})
	thinkingStarted := metrics.tEOU()

	if !background && !isContinuation && admission.Discussion == nil && len(chatmedia.Input(ctx)) == 0 {
		if kind, source, ok := classifyTurn(input, p.routeContext()); ok && routeKindSkipsBrain(kind) {
			return p.completeRoutedTurn(ctx, turn, metrics, input, kind, source)
		}
	}

	if admission.Discussion == nil && p.takeWorkDecision(ctx, pendingDecision, input, metrics) {
		return p.completeRoutedTurn(ctx, turn, metrics, input, RouteSpawn, "decision")
	}

	requestCtx := ctx
	var ordinary *ordinaryCompletion
	if !isContinuation && admission.Discussion == nil {
		requestCtx, ordinary, _ = p.beginOrdinaryCompletion(ctx, input, speakerID)
	}
	if ordinary != nil {
		defer ordinary.close()
		metrics.ordinary = ordinary
		if p.completionNeedsVoice() {
			metrics.delivery = &completionPlayback{lease: ordinary.lease}
		}
		ctx = requestCtx
	}
	// The model deadline is separate from output playback, while both inherit
	// the completion lease fence when this turn carries an update.
	brainCtx, brainCancel := p.withBrainTimeout(ctx)
	defer brainCancel()

	// Peer replies and group discussions have tools disabled; their prose
	// cannot become a fresh user work request through promise repair.
	repair := &promiseRepairState{enabled: len(chatmedia.Input(ctx)) == 0 && !isContinuation && admission.Discussion == nil && p.failedRouteRepairEnabled()}
	streamOpts := brain.StreamOptions{
		Attachments:       chatmedia.Input(ctx),
		Discussion:        admission.Discussion,
		WorkerTiers:       p.workerTierAvailability(),
		WorkerFacts:       p.workerFacts(),
		Identity:          metrics.responseIdentity(),
		OriginalInput:     original,
		Speaker:           speakerID,
		Background:        background,
		AgentContinuation: isContinuation,
		ToolsEnabled:      (p.VoiceToolsEnabled || p.workerTierAvailability() != nil) && !background && !isContinuation && admission.Discussion == nil,
		RouterToolsPolicy: p.liveRouterToolsPolicy(),
		OnToolStart:       p.toolStartHookTracked(&repair.toolsFired, ctx),
		OnToolEnd:         p.toolEndHook(ctx),
		OnUsage:           p.usageHook(),
		OnSessionWarn:     p.sessionWarnHook(),
		OnPromptWarn:      p.promptWarnHook(),
	}
	if len(streamOpts.Attachments) > 0 {
		streamOpts.ToolsEnabled = false
		streamOpts.WorkerTiers = nil
	}
	if isContinuation || background {
		streamOpts.Attachments = nil
	}
	if admission.Discussion != nil {
		streamOpts.WorkerTiers = nil
		streamOpts.WorkerFacts = nil
	}
	if ordinary != nil {
		streamOpts.WorkResults = append([]brain.CompletionResult(nil), ordinary.request.Results...)
	}
	metrics.modelRequest = time.Now()
	p.traceSpan(metrics.trace, traceID(metrics.trace, "model"), "model", "start", "running", metrics.modelRequest, time.Time{}, nil, nil)
	stream, err := p.Brain.ThinkStream(brainCtx, input, streamOpts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return fmt.Errorf("brain: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("brain: %w", err))
		return nil
	}

	if background {
		return p.finishBackgroundStream(ctx, brainCtx, brainCancel, stream, metrics, turn, repair)
	}

	// Stream the reply so the TUI can render it token-by-token, then fall back
	// to the existing whole-response synthesis path below. ResponseStreamingStarted
	// and per-chunk ResponseDelta events are emitted inside observeStream.
	if ordinary != nil {
		stream = ordinary.wrap(stream)
	}
	response, route, err := p.collectTextStream(brainCtx, stream, metrics)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return fmt.Errorf("brain: %w", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Report a deadline failure without trying to speak on the expired context.
		p.recoverTurn(ctx, turn, metrics, response, err)
		return nil
	}
	repair.applyBrainRoute(route)
	_ = repair.shouldRepair(response)
	if kind, source, ok := repair.routedKind(); ok {
		return p.completeRoutedTurn(ctx, turn, metrics, input, kind, source)
	}

	metrics.modelComplete = time.Now()
	p.emitResponse(metrics, events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(thinkingStarted),
	})

	// Voice gate (WI-dc9e33 B4): the full raw response stays in the chat
	// transcript; only voice-safe text may be synthesized. Gate only when the
	// turn would actually speak, so mute never counts phantom leaks.
	//
	// gateForSpeech removes structure the listener should not hear (fences,
	// tool blocks); speakableFromResponse then applies the per-segment cleaning
	// and predicate the progressive path uses, so a typed turn and a spoken one
	// say the same thing.
	speakable := ""
	if !p.OutputMuted() && p.Player != nil && p.ttsReady() {
		speakable = speakableFromResponse(p.gateForSpeech(response, metrics))
	}
	if speakable != "" {
		turn.to(TurnSpeaking)
		now := time.Now()
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = now
		}
		if metrics.firstAdmit.IsZero() {
			metrics.firstAdmit = now
		}
		metrics.ttsEngine = p.ttsIdentity()
		metrics.sinkAtArm = p.sinkKind()
		metrics.audibleSink = metrics.sinkAtArm
		p.warnNoSinkSynth(metrics.sinkAtArm)
		seg := events.SpeechSegmentReady{Text: speakable}
		if !metrics.firstSpeakable.IsZero() && !metrics.firstAdmit.Equal(metrics.firstSpeakable) {
			seg.Elapsed = metrics.elapsedFromEOU(metrics.firstAdmit)
		}
		p.emitResponse(metrics, seg)
		p.emitResponse(metrics, events.GeneratingVoice{Sentence: speakable})

		synthStarted := time.Now()
		// The whole reply is one string here: no group was assembled from a
		// held beat, so its sentence punctuation is spoken as written.
		ctx = withSegmentTrace(ctx, 0)
		stream, usedFallback, observation, err := p.synthUtteranceObserved(ctx, speakable, nil, false)
		if errors.Is(err, errNothingToSpeak) {
			p.completeTextTurn(turn, metrics, response)
			return nil
		}
		if err != nil {
			// Voice is best-effort in text mode: the text response still
			// completed, so the turn is completed (degraded), not failed.
			metrics.degraded = true
			if observation.synthStarted.IsZero() {
				observation.synthStarted = synthStarted
			}
			observation.queuedAt = synthStarted
			observation.textReady = synthStarted
			if observation.stream == nil {
				observation.stream = stream
			}
			failure := synthesisFailureEvent(speakable, 0, observation, err)
			failure.fallback = usedFallback
			metrics.recordSegment(*failure)
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
			p.completeTextTurn(turn, metrics, response)
			return nil
		}
		if observation.synthStarted.IsZero() {
			observation.synthStarted = synthStarted
		}
		observation.queuedAt = synthStarted
		observation.textReady = synthStarted
		if observation.stream == nil {
			observation.stream = stream
		}
		if p.OutputMuted() {
			// Drop the stream so the synth producer is not left blocked on a
			// full frames channel after we skip PlayStream.
			discardPCMStream(stream)
			p.completeTextTurn(turn, metrics, response)
			return nil
		}

		playback, err := p.Player.PlayStream(ctx, stream)
		if err != nil {
			if !usedFallback {
				var fallbackObservation synthesisObservation
				var fallbackUsed bool
				playback, fallbackUsed, fallbackObservation, err = p.playFallbackObserved(ctx, speakable, err, nil, false)
				if fallbackUsed {
					usedFallback = true
					observation = fallbackObservation
				}
			}
			if err == nil {
				// The fallback playback is ready; continue through the normal
				// lifecycle below.
			} else {
				// Engine.PlayStream owns the stream once called (even on error) and
				// must drain it; the pipeline does not discard here.
				metrics.degraded = true
				if observation.synthStarted.IsZero() {
					observation.synthStarted = synthStarted
				}
				observation.queuedAt = synthStarted
				observation.textReady = synthStarted
				failure := synthesisFailureEvent(speakable, 0, observation, err)
				failure.fallback = usedFallback
				metrics.recordSegment(*failure)
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
				p.completeTextTurn(turn, metrics, response)
				return nil
			}
		}
		if observation.synthStarted.IsZero() {
			observation.synthStarted = synthStarted
		}
		observation.queuedAt = synthStarted
		observation.textReady = synthStarted
		if observation.stream == nil {
			observation.stream = stream
		}
		// PlayStream waits for the complete sentence buffer. Output may have
		// been muted while it was waiting, after SetOutputMuted's first Stop
		// saw an empty queue. Stop again so that late-enqueued audio cannot play.
		if p.OutputMuted() {
			p.Player.Stop()
			p.completeTextTurn(turn, metrics, response)
			return nil
		}

		// PlayStream returned, so the utterance is fully synthesized and
		// buffered: everything after this point is playback, not the engine.
		if metrics.delivery != nil {
			metrics.delivery.queued++
		}
		p.handlePlaybackLifecycle(speakable, usedFallback, observation, playback, metrics)
	}

	p.completeAgentResponse(ctx, response, metrics, false)
	p.completeTextTurn(turn, metrics, response)
	return nil
}
