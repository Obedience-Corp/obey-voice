package pipeline

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"os"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

var errPlaybackInterrupted = errors.New("playback interrupted")

// logDegradedTurn writes the underlying error to stderr with a timestamp and
// stage. Under the TUI fd 2 is redirected to native-diagnostics.log, so this
// is the durable record for degraded turns (bus Error flashes and can vanish).
func logDegradedTurn(stage string, err error) {
	fmt.Fprintf(os.Stderr, "%s obey-voice: degraded turn stage=%s: %v\n",
		time.Now().UTC().Format(time.RFC3339), stage, err)
}

// recoverTurn reports the actual failure as a text-only Error event. Preserve
// any generated reply separately; diagnostics must never become persona speech
// or fabricated assistant history. Completing degraded keeps voice input alive.
func (p *Pipeline) recoverTurn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, partial string, err error) {
	metrics.degraded = true
	stage := "brain"
	if errors.Is(err, errPlaybackStalled) {
		stage = "playback"
	}
	p.emit(events.Error{Stage: stage, Message: err.Error()})
	logDegradedTurn(stage, err)
	p.emitResponse(metrics, events.ResponseReady{Response: speakablePartialResponse(partial), Degraded: true})
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

func speakablePartialResponse(partial string) string {
	s := strings.TrimSpace(textclean.StripThinkBlocks(partial))
	if s == "" || textclean.ContainsThinkTag(s) {
		return ""
	}
	return s
}

// speakBestEffort plays a progress utterance, such as a compaction notice.
// Failures are reported as text-only errors.
func (p *Pipeline) speakBestEffort(ctx context.Context, sentence string, metrics *turnMetrics, turn *turnConductor) {
	ctx = audio.BindPlaybackContext(ctx, p.Player)
	if p.OutputMuted() || p.Player == nil || !p.ttsReady() {
		return
	}
	turn.to(TurnSpeaking)
	if metrics.firstSegment.IsZero() {
		metrics.firstSegment = time.Now()
	}
	p.emitResponse(metrics, events.SpeechSegmentReady{Text: sentence})
	p.emitResponse(metrics, events.GeneratingVoice{Sentence: sentence})

	if metrics.ttsEngine.provider == "" {
		metrics.ttsEngine = p.ttsIdentity()
	}
	synthStarted := time.Now()
	stream, usedFallback, observation, err := p.synthesizeWithFallbackObserved(ctx, sentence, nil, false)
	if errors.Is(err, errNothingToSpeak) {
		return
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		return
	}
	if observation.synthStarted.IsZero() {
		observation.synthStarted = synthStarted
	}
	observation.queuedAt = synthStarted
	observation.textReady = synthStarted
	observation.stream = stream
	if p.OutputMuted() {
		discardPCMStream(stream)
		return
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
		return
	}
	p.handlePlaybackLifecycle(sentence, usedFallback, observation, playback, metrics)
}

// speakStandalone synthesizes one short utterance outside the streaming path.
// turn may be nil for idle job summaries.
func (p *Pipeline) speakStandalone(ctx context.Context, turn *turnConductor, metrics *turnMetrics, sentence string) error {
	ctx = audio.BindPlaybackContext(ctx, p.Player)
	sentence = strings.TrimSpace(sentence)
	if sentence != "" && metrics != nil && metrics.routedSpeech != nil {
		*metrics.routedSpeech = append(*metrics.routedSpeech, sentence)
	}
	primary, _ := p.responseTTS(ctx)
	if sentence == "" || p.OutputMuted() || p.Player == nil || primary == nil || !primary.Available() {
		return nil
	}
	if turn != nil {
		turn.to(TurnSpeaking)
	}
	if metrics != nil && metrics.firstSegment.IsZero() {
		metrics.firstSegment = time.Now()
	}
	p.emitResponse(metrics, events.SpeechSegmentReady{Text: sentence})
	p.emitResponse(metrics, events.GeneratingVoice{Sentence: sentence})

	synthStarted := time.Now()
	stream, usedFallback, observation, err := p.synthesizeWithFallbackObserved(ctx, sentence, nil, false)
	if errors.Is(err, errNothingToSpeak) {
		return nil
	}
	if err != nil {
		p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		return err
	}
	if observation.synthStarted.IsZero() {
		observation.synthStarted = synthStarted
	}
	observation.queuedAt = synthStarted
	observation.textReady = synthStarted
	observation.stream = stream
	if p.OutputMuted() {
		discardPCMStream(stream)
		return nil
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
		return err
	}
	if metrics != nil {
		if metrics.ttsEngine.provider == "" {
			metrics.ttsEngine = p.ttsIdentity()
		}
		p.handlePlaybackLifecycle(sentence, usedFallback, observation, playback, metrics)
	} else {
		result := <-playback.Done()
		if result.Interrupted {
			return errPlaybackInterrupted
		}
	}
	return nil
}
