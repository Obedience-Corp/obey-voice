package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// errNothingToSpeak is a tag-only beat on an engine that cannot vocalize it.
// Callers skip the segment instead of sending empty text or Kokoro-spelling.
var errNothingToSpeak = errors.New("nothing to synthesize")

// EnableVocals installs the optional recorded-sound bank. Empty dir is fine:
// [moan]/[laugh] expand to onomatopoeia and go through the live TTS throat.
// WAV files in dir (moan.wav, gasp.wav, …) replace a kind with a recording.
func (p *Pipeline) EnableVocals(dir string) {
	p.vocals = newVocalBank(dir)
}

func collectPCM(ctx context.Context, stream *audio.PCMStream) ([]float32, int, error) {
	if stream == nil {
		return nil, 0, errors.New("nil pcm stream")
	}
	rate, err := stream.WaitReady(ctx)
	if err != nil {
		return nil, 0, err
	}
	var samples []float32
	for frame := range stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := stream.Err(); err != nil {
		return nil, rate, err
	}
	return samples, rate, nil
}

// pcmStreamFromSamples publishes an already-complete buffer as a stream.
//
// It deliberately does NOT derive from whatever stream those samples came out
// of, even though it looks like a re-frame. The samples are in memory before
// this is called, so the result is whole-utterance by construction: carrying an
// incremental flag would tell the player to start on a partial buffer that was
// filled in one burst, and carrying a stall budget would arm a silence timer
// against a producer that cannot be silent.
func pcmStreamFromSamples(ctx context.Context, samples []float32, rate int) *audio.PCMStream {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		stream.CloseWithError(err)
		return stream
	}
	go func() {
		defer stream.Close()
		_ = stream.Write(samples)
	}()
	return stream
}

func (p *Pipeline) synthUtteranceObserved(ctx context.Context, sentence string, stickFallback *bool) (*audio.PCMStream, bool, synthesisObservation, error) {
	parts := splitVocalizations(sentence)
	// Recorded WAVs (persona sounds/) splice in on any engine. Otherwise
	// tags become onomatopoeia only for a provider that can vocalize them.
	if p.vocals != nil && p.vocals.allSoundsRecorded(parts) {
		return p.renderVocalizedObserved(ctx, parts, stickFallback)
	}
	return p.synthesizeWithFallbackObserved(ctx, sentence, stickFallback)
}

// prepareSpeechForTTS is the text one provider should hear. Qwen gets the
// onomatopoeia; Kokoro and other engines get the tags stripped.
func prepareSpeechForTTS(sentence string, provider tts.Provider, vocals *vocalBank) string {
	if !hasVocalTag(sentence) {
		return sentence
	}
	if tts.RendersVocalTags(provider) {
		pick := firstVocalPhrase
		if vocals != nil {
			pick = vocals.nextPhrase
		}
		return expandVocalTags(sentence, pick)
	}
	return stripVocalTags(sentence)
}

// renderVocalizedObserved stitches sound clips and synthesized speech into one stream
// so a reply like "[moan] that's it" is a moan then words, not the word "moan".

func (p *Pipeline) renderVocalizedObserved(ctx context.Context, parts []vocalPart, stickFallback *bool) (*audio.PCMStream, bool, synthesisObservation, error) {
	var chunks [][]float32
	usedFallback := false
	observation := synthesisObservation{warmState: events.WarmStateUnknown}
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, usedFallback, observation, err
		}
		if part.sound != "" {
			clip, ok := p.vocals.pick(part.sound)
			if !ok {
				continue
			}
			chunks = append(chunks, clip.samples)
			continue
		}
		text := strings.TrimSpace(part.text)
		if text == "" {
			continue
		}
		stream, fb, partObservation, err := p.synthesizeWithFallbackObserved(ctx, text, stickFallback)
		if err != nil {
			return nil, fb, partObservation, err
		}
		observation = partObservation
		if fb {
			usedFallback = true
		}
		samples, rate, err := collectPCM(ctx, stream)
		if err != nil {
			return nil, usedFallback, observation, err
		}
		if rate != vocalSampleRate && rate > 0 {
			samples = resampleLinear(samples, rate, vocalSampleRate)
		}
		chunks = append(chunks, samples)
	}
	mixed := crossfadeConcat(chunks, vocalSampleRate)
	if len(mixed) == 0 {
		return nil, usedFallback, observation, errors.New("vocalized segment produced no audio")
	}
	observation.wholeSentence = true
	return pcmStreamFromSamples(ctx, mixed, vocalSampleRate), usedFallback, observation, nil
}
