package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	p.incActiveTurn()
	defer p.decActiveTurn()
	if p.BeforeCapture != nil {
		if err := p.BeforeCapture(ctx); err != nil {
			return "", fmt.Errorf("prepare capture binding: %w", err)
		}
	}
	conversation := p.Native != nil && p.Native.Controls == "conversation"
	input, err := p.runVoiceWithContinuations(ctx)
	if conversation {
		return "", err
	} // Never execute host post-turn spoken controls.
	return input, err
}

func (p *Pipeline) runTurnOnce(ctx context.Context) (string, error) {
	p.incActiveTurn()
	defer p.decActiveTurn()
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	defer p.releaseUncommittedInput(metrics)
	defer func() { p.finishNativeCaption(ctx, metrics) }()
	defer func() {
		if err := p.persistCanonicalNativeInput(ctx, metrics); err != nil {
			p.emit(events.Error{Stage: "session_audio", Message: err.Error()})
		}
	}()
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics, ctx)
	ctx = events.WithTrace(ctx, metrics.trace)

	continuation, isContinuation := continuationFromContext(ctx)
	if !isContinuation && p.Player != nil {
		p.Player.Stop()
	}

	var text string
	var err error
	if isContinuation {
		text = continuation.admission.Request
	} else {
		metrics.captureBound = true
		metrics.capturePolicy = p.Native
		turn.to(TurnListening)
		text, err = p.transcribeTurn(ctx, metrics, turn)
	}
	// An EndUtterance that arrived after listening stopped was meant for this
	// turn; dropping it keeps it from cutting the next one short.
	if !isContinuation && p.Utterance != nil {
		p.Utterance.Clear()
	}
	if errors.Is(err, errYieldedForCompletion) {
		turn.finish(TurnYielded)
		return "", nil
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
		} else {
			turn.finish(TurnFailed)
		}
		return "", err
	}
	if !isContinuation && text == "" && metrics.input == nil {
		turn.finish(TurnTimedOut)
		return "", nil
	}
	nativeConversation := metrics.input != nil && p.Native != nil && p.Native.Controls == "conversation"
	// Name summon (D5). Ahead of the backchannel on purpose: speech meant for
	// someone else in the room must not even get an "mm-hm" back.
	if !isContinuation && !nativeConversation && p.AddressGate != nil {
		if reason := p.AddressGate(text); reason != "" {
			p.emit(events.Info{Message: reason})
			turn.finish(TurnTimedOut)
			return "", nil
		}
	}

	turn.to(TurnThinking)
	// The wait a backchannel covers starts here: the user has stopped talking
	// and nothing will be heard until the brain and TTS finish. No-op unless
	// enabled and unless recent turns were actually slow.
	speakerID := p.currentSpeakerID()
	if !isContinuation && p.SpeakerGate != nil {
		if reason := p.SpeakerGate(speakerID); reason != "" {
			// Reuses TurnTimedOut's "no usable speech" terminal semantics;
			// the Info event carries the actual veto reason.
			p.emit(events.Info{Message: reason})
			turn.finish(TurnTimedOut)
			return "", nil
		}
	}
	if !isContinuation {
		p.emitResponse(metrics, events.UserInput{Text: text, Speaker: speakerID, Utterance: metrics.input.Clone()})
	}
	original := text
	var pendingDecision *decisionTurn
	if !p.GroupParticipationEnabled && !nativeConversation {
		pendingDecision = p.startWorkDecision(ctx, text, decision.VoiceWork, isContinuation)
	}
	defer func() { pendingDecision.close() }()
	var admission ResponseAdmission
	if isContinuation {
		admission = continuation.admission
		ctx = p.bindAdmission(ctx, "", admission, metrics)
		speakerID = ""
	} else if nativeConversation {
		err = p.validateCapturedBinding(metrics)
		if err == nil && p.AdmitAudio != nil {
			admission, err = p.AdmitAudio(ctx, metrics.input.Clone())
		}
		if err == nil {
			err = p.validateCapturedBinding(metrics)
		}
		if err == nil {
			ctx = p.bindAdmission(ctx, text, admission, metrics)
		}
	} else {
		ctx, admission, err = p.admitInput(ctx, text, false, metrics)
		if err != nil {
			turn.finish(TurnFailed)
			return original, wrapAdmissionError(err)
		}
	}
	if err != nil {
		turn.finish(TurnFailed)
		return original, wrapAdmissionError(err)
	}
	if admission.Skip {
		turn.finish(TurnYielded)
		return original, nil
	}
	if admission.Discussion != nil {
		var stop func()
		ctx, stop = discussion.Bind(ctx, admission.Discussion.State)
		defer stop()
	}
	if p.GroupParticipationEnabled && admission.Discussion == nil && !nativeConversation {
		pendingDecision = p.startWorkDecision(ctx, text, decision.VoiceWork, isContinuation)
	}
	if !isContinuation && admission.LocalReply != "" {
		return original, p.completeLocalReply(ctx, original, admission.LocalReply, metrics, turn)
	}
	text = admission.Request
	if err := p.validateRetainedImages(ctx); err != nil {
		turn.finish(TurnFailed)
		return original, err
	}
	if !isContinuation {
		p.playBackchannel(ctx)
	}
	if err := p.prepareTurn(ctx, text, metrics, turn); err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return original, err
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("compact: %w", err))
		return original, nil
	}
	p.emitResponse(metrics, events.ThinkingStarted{})

	// Layer 1 — deterministic phrase and work-intent routing before the live
	// brain runs, so tool work never occupies the talking context window.
	if kind, source, ok := classifyTurn(text, p.routeContext()); !isContinuation && !nativeConversation && admission.Discussion == nil && ok && routeKindSkipsBrain(kind) {
		return original, p.completeRoutedTurn(ctx, turn, metrics, text, kind, source)
	}

	if !nativeConversation && admission.Discussion == nil && p.takeWorkDecision(ctx, pendingDecision, text, metrics) {
		return original, p.completeRoutedTurn(ctx, turn, metrics, text, RouteSpawn, "decision")
	}

	// turnCtx scopes the whole turn so the playback watchdog can abort the brain
	// stream too — not just the derived playback context. Brain work is further
	// bounded so a hung model after tools cannot wedge the conversation loop.
	turnCtx, turnCancel := p.withBrainTimeout(ctx)
	defer turnCancel()
	requestCtx := turnCtx
	var ordinary *ordinaryCompletion
	var leaseErr error
	if !isContinuation && admission.Discussion == nil {
		requestCtx, ordinary, leaseErr = p.beginOrdinaryCompletion(turnCtx, text, speakerID)
	}
	if leaseErr != nil {
		requestCtx = turnCtx
	}
	if ordinary != nil {
		defer ordinary.close()
		metrics.ordinary = ordinary
		metrics.delivery = &completionPlayback{lease: ordinary.lease}
	}

	// Peer replies and group discussions have tools disabled; their prose
	// cannot become a fresh user work request through promise repair.
	repair := &promiseRepairState{enabled: !nativeConversation && !isContinuation && admission.Discussion == nil && p.failedRouteRepairEnabled()}
	streamOpts := brain.StreamOptions{
		Utterance:         metrics.input,
		AudioRetained:     metrics.audioRetained,
		Discussion:        admission.Discussion,
		WorkerTiers:       p.workerTierAvailability(),
		WorkerFacts:       p.workerFacts(),
		Identity:          metrics.responseIdentity(),
		OriginalInput:     original,
		AgentContinuation: isContinuation,
		VoiceMode:         true,
		Speaker:           speakerID,
		ToolsEnabled:      !nativeConversation && admission.Discussion == nil && !isContinuation && (p.VoiceToolsEnabled || p.workerTierAvailability() != nil),
		RouterToolsPolicy: p.liveRouterToolsPolicy(),
		OnToolStart:       p.toolStartHookTracked(&repair.toolsFired, ctx),
		OnToolEnd:         p.toolEndHook(ctx),
		OnUsage:           p.usageHook(),
		OnSessionWarn:     p.sessionWarnHook(),
		OnPromptWarn:      p.promptWarnHook(),
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
	modelInput := text
	if metrics.input != nil {
		modelInput = ""
	}
	brainStream, err := p.startBrainStream(requestCtx, modelInput, streamOpts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return original, fmt.Errorf("brain: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("brain: %w", err))
		return original, nil
	}

	if ordinary != nil {
		brainStream = ordinary.wrap(brainStream)
	}
	fullResponse, interrupted, err := p.streamResponse(requestCtx, turnCancel, brainStream, true, metrics, turn, repair)
	if kind, source, ok := repair.routedKind(); ok {
		if p.Player != nil {
			p.Player.Stop()
		}
		return original, p.completeRoutedTurn(ctx, turn, metrics, text, kind, source)
	}
	if err != nil {
		if interrupted || errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return original, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// Model/tool stall — distinct from STT timed_out (no speech).
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Report the failure without attempting speech on the expired context.
		p.recoverTurn(ctx, turn, metrics, fullResponse, err)
		return original, nil
	}
	p.finishOrdinaryCompletion(metrics, fullResponse, interrupted)

	// On a barge-in the user is already speaking their next turn into the mic;
	// keep that audio instead of draining it when the next listen begins. A
	// near-miss (F3.3) may already have set keepCapture inside streamResponse.
	if interrupted {
		p.keepCapture.Store(true)
		p.clearPreparedCapture()
	} else {
		// Establish the next listen scope before handoff classification starts.
		// Audio arriving while the classifier runs is then appended to this
		// bounded tail instead of being lost to the next unconditional reset.
		p.prepareNextListen()
	}

	p.completeAgentResponse(ctx, fullResponse, metrics, interrupted)
	p.emitResponse(metrics, events.ResponseReady{
		Response:    fullResponse,
		Interrupted: interrupted,
		Degraded:    metrics.degraded,
	})
	if interrupted {
		turn.finish(TurnInterrupted)
	} else {
		turn.finish(TurnCompleted)
	}

	if p.OnTurn != nil {
		p.OnTurn()
	}
	metrics.captionEligible = !interrupted && !metrics.degraded && ctx.Err() == nil

	return original, nil
}

func (p *Pipeline) transcribeTurn(ctx context.Context, metrics *turnMetrics, turn *turnConductor) (text string, err error) {
	endTrace := p.startTraceBoundary(ctx, "stt")
	defer func() {
		status := "unknown"
		if !metrics.sttFinal.IsZero() {
			status = "completed"
		} else if ctx.Err() != nil {
			status = "cancelled"
		} else if err != nil {
			status = "failed"
		}
		boundary := time.Now()
		if status == "completed" {
			boundary = metrics.sttFinal
		}
		endTrace(status, boundary)
	}()
	if p.STT == nil && p.Input == nil {
		return "", errors.New("STT provider is not configured")
	}

	session, generation, err := p.startCaptureSession(ctx)
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	yielded := false
	defer func() { p.finishCaptureSession(session, generation, yielded) }()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", nil
			}

			if finalized, ok := event.(stt.FinalizedInput); ok {
				metrics.sttFinal = time.Now()
				return p.resolveNativeInput(ctx, finalized.Input, metrics)
			}

			// InputLevel is droppable UI meter data; handle before the typed
			// switch so high-rate samples never participate in turn control.
			if lvl, ok := event.(stt.InputLevel); ok {
				p.emit(events.AudioLevel{Source: "input", Level: lvl.Level})
				continue
			}

			te := stt.ToTyped(event)
			switch te.Kind {
			case stt.KindPhase:
				if p.Native != nil && te.Phase == "hearing" {
					if err := p.prepareNativeForeground(ctx); err != nil {
						return "", err
					}
				}
				if te.Phase == "transcribing" {
					turn.to(TurnTranscribing)
				}
				p.emitResponse(metrics, events.STTPhase{
					Phase:   te.Phase,
					Elapsed: te.Elapsed,
				})
			case stt.KindPartialTranscript:
				if te.Text != "" {
					p.emit(events.TranscriptPartial{Text: te.Text})
				}
			case stt.KindFinalTranscript:
				metrics.sttFinal = time.Now()
				metrics.acousticName = te.AcousticName
				return te.Text, nil
			case stt.KindYieldedForCompletion:
				if event, ok := event.(stt.YieldedForCompletion); ok {
					p.captureMu.Lock()
					if p.captureSession != nil && p.captureGeneration == generation {
						p.captureToken = event.Token
					}
					p.captureMu.Unlock()
				}
				yielded = true
				return "", errYieldedForCompletion
			case stt.KindTimeout:
				return "", nil
			case stt.KindFailure:
				// Failure needs the error value (errors.Is + %w), not ErrText.
				fail, _ := event.(stt.Failure)
				if errors.Is(fail.Err, context.Canceled) && ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", fmt.Errorf("STT: %w", fail.Err)
			case stt.KindInputLevel:
				p.emit(events.AudioLevel{Source: "input", Level: te.Level})
			}
		}
	}
}
