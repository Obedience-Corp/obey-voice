package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// logDegradedTurn writes the underlying error to stderr with a timestamp and
// stage. Under the TUI fd 2 is redirected to native-diagnostics.log, so this
// is the durable record for degraded turns (bus Error flashes and can vanish).
func logDegradedTurn(stage string, err error) {
	fmt.Fprintf(os.Stderr, "%s obey-voice: degraded turn stage=%s: %v\n",
		time.Now().UTC().Format(time.RFC3339), stage, err)
}

// recoverTurn closes out a turn that died on a hard brain/tool failure with a
// user-visible (and spoken, when possible) recovery reply instead of silence.
// The error becomes activity detail, the transcript keeps any partial reply
// with the recovery line appended, and the turn completes degraded — so
// callers do not count a model failure as a voice-input failure.
func (p *Pipeline) recoverTurn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, partial string, err error) {
	metrics.degraded = true
	stage := "brain"
	speak := true
	playbackStall := errors.Is(err, errPlaybackStalled)
	if playbackStall {
		// The player is wedged; queueing recovery speech into it would stall
		// again. The reply text still reaches the transcript.
		stage = "playback"
		speak = false
	}
	p.emit(events.Error{Stage: stage, Message: err.Error()})
	logDegradedTurn(stage, err)

	partial = strings.TrimSpace(partial)
	response := brain.RecoveryReply
	if playbackStall {
		// Whole-utterance TTS often delivers audio after the watchdog fires.
		// Appending RecoveryReply is how 2026-08-21 showed Veronica's line plus
		// "Want me to try a simpler approach?" on the same turn. The Error
		// event above is the activity detail; the transcript keeps what was
		// already spoken — speakable content only, never a think region.
		response = speakableStallTranscript(partial)
	} else if partial != "" {
		// Ollama's stream layer may already have recovered out loud (streaming
		// RecoveryReply itself) before the turn died; appending a second copy
		// is how the field capture showed the same apology twice.
		response = partial
		if !strings.HasSuffix(partial, brain.RecoveryReply) {
			response = partial + "\n\n" + brain.RecoveryReply
		}
	}
	p.emit(events.ResponseReady{Response: response, Degraded: true})

	if speak && ctx.Err() == nil {
		p.speakBestEffort(ctx, brain.RecoveryReply, metrics, turn)
	}
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

func speakableStallTranscript(partial string) string {
	s := strings.TrimSpace(textclean.StripThinkBlocks(partial))
	if s == "" || textclean.ContainsThinkTag(s) {
		return ""
	}
	return s
}

// speakBestEffort synthesizes and plays one utterance for a degraded turn.
// Failures only produce activity detail — recovery speech must never fail the
// turn it is rescuing.
func (p *Pipeline) speakBestEffort(ctx context.Context, sentence string, metrics *turnMetrics, turn *turnConductor) {
	if p.OutputMuted() || p.Player == nil || !p.ttsReady() {
		return
	}
	turn.to(TurnSpeaking)
	if metrics.firstSegment.IsZero() {
		metrics.firstSegment = time.Now()
	}
	p.emit(events.SpeechSegmentReady{Text: sentence})
	p.emit(events.GeneratingVoice{Sentence: sentence})

	if metrics.ttsEngine.provider == "" {
		metrics.ttsEngine = p.ttsIdentity()
	}
	synthStarted := time.Now()
	stream, usedFallback, err := p.synthesizeWithFallback(ctx, sentence, nil)
	if err != nil {
		p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		return
	}
	if p.OutputMuted() {
		discardPCMStream(stream)
		return
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
		return
	}
	p.handlePlaybackLifecycle(sentence, usedFallback, synthStarted, time.Since(synthStarted), playback, metrics)
}

// speakStandalone synthesizes one short utterance outside the streaming path.
// turn may be nil for idle job summaries.
func (p *Pipeline) speakStandalone(ctx context.Context, turn *turnConductor, metrics *turnMetrics, sentence string) error {
	sentence = strings.TrimSpace(sentence)
	if sentence == "" || p.OutputMuted() || p.Player == nil || !p.ttsReady() {
		return nil
	}
	if turn != nil {
		turn.to(TurnSpeaking)
	}
	if metrics != nil && metrics.firstSegment.IsZero() {
		metrics.firstSegment = time.Now()
	}
	p.emit(events.SpeechSegmentReady{Text: sentence})
	p.emit(events.GeneratingVoice{Sentence: sentence})

	stream, usedFallback, err := p.synthesizeWithFallback(ctx, sentence, nil)
	if err != nil {
		p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		return err
	}
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
		synthStarted := time.Now()
		p.handlePlaybackLifecycle(sentence, usedFallback, synthStarted, time.Since(synthStarted), playback, metrics)
	} else {
		<-playback.Done()
	}
	return nil
}
