package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	p.incActiveTurn()
	defer p.decActiveTurn()
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics)

	if p.Player != nil {
		p.Player.Stop()
	}

	turn.to(TurnListening)
	text, err := p.transcribeTurn(ctx, metrics, turn)
	// An EndUtterance that arrived after listening stopped was meant for this
	// turn; dropping it keeps it from cutting the next one short.
	if p.Utterance != nil {
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
	if text == "" {
		turn.finish(TurnTimedOut)
		return "", nil
	}
	// Name summon (D5). Ahead of the backchannel on purpose: speech meant for
	// someone else in the room must not even get an "mm-hm" back.
	if p.AddressGate != nil {
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
	p.playBackchannel(ctx)
	speakerID := p.currentSpeakerID()
	if p.SpeakerGate != nil {
		if reason := p.SpeakerGate(speakerID); reason != "" {
			// Reuses TurnTimedOut's "no usable speech" terminal semantics;
			// the Info event carries the actual veto reason.
			p.emit(events.Info{Message: reason})
			turn.finish(TurnTimedOut)
			return "", nil
		}
	}
	p.emit(events.UserInput{Text: text, Speaker: speakerID})
	if err := p.prepareTurn(ctx, text, metrics, turn); err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return text, err
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("compact: %w", err))
		return text, nil
	}
	p.emit(events.ThinkingStarted{})

	// Layer 1 — deterministic phrase and work-intent routing before the live
	// brain runs, so tool work never occupies the talking context window.
	if kind, source, ok := classifyTurn(text, p.routeContext()); ok && routeKindSkipsBrain(kind) {
		p.completeRoutedTurn(ctx, turn, metrics, text, kind, source)
		return text, nil
	}

	// turnCtx scopes the whole turn so the playback watchdog can abort the brain
	// stream too — not just the derived playback context. Brain work is further
	// bounded so a hung model after tools cannot wedge the conversation loop.
	turnCtx, turnCancel := p.withBrainTimeout(ctx)
	defer turnCancel()

	repair := &promiseRepairState{enabled: p.VoiceFailedRouteRepair}
	streamOpts := brain.StreamOptions{
		VoiceMode:         true,
		Speaker:           speakerID,
		ToolsEnabled:      p.VoiceToolsEnabled,
		RouterToolsPolicy: p.liveRouterToolsPolicy(),
		OnToolStart:       p.toolStartHookTracked(&repair.toolsFired),
		OnToolEnd:         p.toolEndHook(),
		OnUsage:           p.usageHook(),
		OnSessionWarn:     p.sessionWarnHook(),
		OnPromptWarn:      p.promptWarnHook(),
	}

	brainStream, err := p.Brain.ThinkStream(turnCtx, text, streamOpts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return text, fmt.Errorf("brain: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("brain: %w", err))
		return text, nil
	}

	fullResponse, interrupted, err := p.streamResponse(turnCtx, turnCancel, brainStream, true, metrics, turn, repair)
	if kind, source, ok := repair.routedKind(); ok {
		if p.Player != nil {
			p.Player.Stop()
		}
		p.completeRoutedTurn(ctx, turn, metrics, text, kind, source)
		return text, nil
	}
	if err != nil {
		if interrupted || errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return text, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// Model/tool stall — distinct from STT timed_out (no speech).
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Speak on the parent ctx: turnCtx is dead after a deadline stall.
		p.recoverTurn(ctx, turn, metrics, fullResponse, err)
		return text, nil
	}

	// On a barge-in the user is already speaking their next turn into the mic;
	// keep that audio instead of draining it when the next listen begins. A
	// near-miss (F3.3) may already have set keepCapture inside streamResponse.
	if interrupted {
		p.keepCapture.Store(true)
	}

	p.emit(events.ResponseReady{
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

	return text, nil
}

func (p *Pipeline) transcribeTurn(ctx context.Context, metrics *turnMetrics, turn *turnConductor) (string, error) {
	if p.STT == nil {
		return "", errors.New("STT provider is not configured")
	}

	session, err := p.startCaptureSession(ctx)
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	yielded := false
	defer func() { p.finishCaptureSession(session, yielded) }()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", nil
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
				if te.Phase == "transcribing" {
					turn.to(TurnTranscribing)
				}
				p.emit(events.STTPhase{
					Phase:   te.Phase,
					Elapsed: te.Elapsed,
				})
			case stt.KindPartialTranscript:
				if te.Text != "" {
					p.emit(events.TranscriptPartial{Text: te.Text})
				}
			case stt.KindFinalTranscript:
				metrics.sttFinal = time.Now()
				return te.Text, nil
			case stt.KindYieldedForCompletion:
				if event, ok := event.(stt.YieldedForCompletion); ok {
					p.captureMu.Lock()
					if p.captureSession == session {
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
