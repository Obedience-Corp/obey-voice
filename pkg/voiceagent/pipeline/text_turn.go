package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// completeTextTurn finishes a successful text-mode turn exactly once:
// ResponseReady for the transcript, terminal metrics via finish, and OnTurn
// for session auto-save. Every degraded and mute short-circuit must use this
// so finish logic cannot drift across early returns.
func (p *Pipeline) completeTextTurn(turn *turnConductor, metrics *turnMetrics, response string) {
	p.emit(events.ResponseReady{Response: response, Degraded: metrics.degraded})
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	return p.runTextTurn(ctx, input, false)
}

func (p *Pipeline) runTextTurn(ctx context.Context, input string, background bool) error {
	p.incActiveTurn()
	defer p.decActiveTurn()
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics)

	turn.to(TurnThinking)
	speakerID := p.currentSpeakerID()
	if background {
		speakerID = ""
	} else {
		p.emit(events.UserInput{Text: input, Speaker: speakerID})
	}
	if err := p.prepareTurn(ctx, input, metrics, turn); err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return err
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("compact: %w", err))
		return nil
	}
	p.emit(events.ThinkingStarted{})
	if metrics.sttFinal.IsZero() {
		metrics.sttFinal = time.Now()
	}
	thinkingStarted := metrics.sttFinal

	if !background {
		if kind, source, ok := classifyTurn(input, p.routeContext()); ok && routeKindSkipsBrain(kind) {
			p.completeRoutedTurn(ctx, turn, metrics, input, kind, source)
			return nil
		}
	}

	// Bound model + tools so a hung Ollama chat after tool results cannot leave
	// the TUI forever on "thinking" / last tool status.
	brainCtx, brainCancel := p.withBrainTimeout(ctx)
	defer brainCancel()

	repair := &promiseRepairState{enabled: p.VoiceFailedRouteRepair}
	stream, err := p.Brain.ThinkStream(brainCtx, input, brain.StreamOptions{
		Speaker:           speakerID,
		Background:        background,
		ToolsEnabled:      p.VoiceToolsEnabled && !background,
		RouterToolsPolicy: p.liveRouterToolsPolicy(),
		OnToolStart:       p.toolStartHookTracked(&repair.toolsFired),
		OnToolEnd:         p.toolEndHook(),
		OnUsage:           p.usageHook(),
		OnSessionWarn:     p.sessionWarnHook(),
		OnPromptWarn:      p.promptWarnHook(),
	})
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
	response, route, err := p.collectTextStream(brainCtx, stream, metrics)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return fmt.Errorf("brain: %w", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Speak on the parent ctx: brainCtx is dead after a deadline stall.
		p.recoverTurn(ctx, turn, metrics, response, err)
		return nil
	}
	repair.applyBrainRoute(route)
	_ = repair.shouldRepair(response)
	if kind, source, ok := repair.routedKind(); ok {
		p.completeRoutedTurn(ctx, turn, metrics, input, kind, source)
		return nil
	}

	metrics.modelComplete = time.Now()
	p.emit(events.ThinkingComplete{
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
		p.emit(seg)
		p.emit(events.GeneratingVoice{Sentence: speakable})

		synthStarted := time.Now()
		stream, usedFallback, err := p.synthUtterance(ctx, speakable, nil)
		if err != nil {
			// Voice is best-effort in text mode: the text response still
			// completed, so the turn is completed (degraded), not failed.
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
			p.completeTextTurn(turn, metrics, response)
			return nil
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
				playback, usedFallback, err = p.playFallback(ctx, speakable, err, nil)
			}
			if err == nil {
				// The fallback playback is ready; continue through the normal
				// lifecycle below.
			} else {
				// Engine.PlayStream owns the stream once called (even on error) and
				// must drain it; the pipeline does not discard here.
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
				p.completeTextTurn(turn, metrics, response)
				return nil
			}
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
		p.handlePlaybackLifecycle(speakable, usedFallback, synthStarted, time.Since(synthStarted), playback, metrics)
	}

	p.completeTextTurn(turn, metrics, response)
	return nil
}
