package audio

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Defaults for file transcription. They are deliberately not the live capture
// values: a file is transcribed for captions, where a shorter silence gap keeps
// cues near their sentence and a hard cap keeps one decode bounded.
const (
	// defaultFileMinSilence closes a speech segment after this much silence.
	defaultFileMinSilence = 600 * time.Millisecond
	// defaultFileMaxSpeech splits continuous speech into decodable spans.
	defaultFileMaxSpeech = 20 * time.Second
)

// FileVADOptions tunes the file detector. A zero field takes the default.
type FileVADOptions struct {
	MinSilence time.Duration
	MaxSpeech  time.Duration
}

// NewFileVAD builds a detector for transcribing a finite file.
//
// The caller must never call Clear on it while a file is in flight. Clear calls
// native Reset, which zeroes the sample origin every reported segment start is
// measured from, so a reset mid-file silently rebases every later timestamp.
// Build one detector per file, feed it to the end, Flush, then Delete it.
//
// cfg supplies the models directory and nothing else: the live capture keys
// (vad_threshold, vad_silence_duration, vad_min_speech_duration) do not apply,
// so a user's live tuning cannot change the timing of a transcript.
func NewFileVAD(cfg *config.Config, opts FileVADOptions) (*VAD, error) {
	params, err := fileVADParams(opts)
	if err != nil {
		return nil, err
	}
	return newVAD(cfg, params)
}

// Resolved returns the options with every zero field replaced by the default
// NewFileVAD would apply. A caller that has to size a buffer against the
// detector's real behavior (the transcription span detector sizes its pre-roll
// ring that way) reads the same numbers the detector runs with.
func (o FileVADOptions) Resolved() FileVADOptions {
	if o.MinSilence == 0 {
		o.MinSilence = defaultFileMinSilence
	}
	if o.MaxSpeech == 0 {
		o.MaxSpeech = defaultFileMaxSpeech
	}
	return o
}

// fileVADParams resolves options to detector parameters. It is separate from
// NewFileVAD so defaulting and validation are testable without the model.
func fileVADParams(opts FileVADOptions) (vadParams, error) {
	if opts.MinSilence < 0 {
		return vadParams{}, fmt.Errorf("audio: file VAD MinSilence %s is negative", opts.MinSilence)
	}
	if opts.MaxSpeech < 0 {
		return vadParams{}, fmt.Errorf("audio: file VAD MaxSpeech %s is negative", opts.MaxSpeech)
	}
	resolved := opts.Resolved()
	minSilence, maxSpeech := resolved.MinSilence, resolved.MaxSpeech
	if limit := vadBufferSeconds * time.Second; maxSpeech > limit {
		return vadParams{}, fmt.Errorf("audio: file VAD MaxSpeech %s exceeds the detector's %s buffer", maxSpeech, limit)
	}
	if minSpeech := time.Duration(vadMinSpeechDuration * float64(time.Second)); maxSpeech < minSpeech {
		return vadParams{}, fmt.Errorf("audio: file VAD MaxSpeech %s is below the %s minimum speech duration", maxSpeech, minSpeech)
	}
	return vadParams{
		threshold:  vadThreshold,
		minSpeech:  vadMinSpeechDuration,
		minSilence: float32(minSilence.Seconds()),
		maxSpeech:  float32(maxSpeech.Seconds()),
	}, nil
}
