package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

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

func (p *Pipeline) synthUtterance(ctx context.Context, sentence string, stickFallback *bool) (*audio.PCMStream, bool, error) {
	parts := splitVocalizations(sentence)
	// Recorded WAVs (persona sounds/) splice in. Otherwise expand tags to the
	// onomatopoeia Qwen vocalizes in this speaker — one take, same throat.
	if p.vocals != nil && p.vocals.allSoundsRecorded(parts) {
		return p.renderVocalized(ctx, parts, stickFallback)
	}
	pick := firstVocalPhrase
	if p.vocals != nil {
		pick = p.vocals.nextPhrase
	}
	text := expandVocalTags(sentence, pick)
	if text == "" {
		return nil, false, errors.New("nothing to synthesize")
	}
	return p.synthesizeWithFallback(ctx, text, stickFallback)
}

// renderVocalized stitches sound clips and synthesized speech into one stream
// so a reply like "[moan] that's it" is a moan then words, not the word "moan".
func (p *Pipeline) renderVocalized(ctx context.Context, parts []vocalPart, stickFallback *bool) (*audio.PCMStream, bool, error) {
	var chunks [][]float32
	usedFallback := false
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, usedFallback, err
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
		stream, fb, err := p.synthesizeWithFallback(ctx, text, stickFallback)
		if err != nil {
			return nil, fb, err
		}
		if fb {
			usedFallback = true
		}
		samples, rate, err := collectPCM(ctx, stream)
		if err != nil {
			return nil, usedFallback, err
		}
		if rate != vocalSampleRate && rate > 0 {
			samples = resampleLinear(samples, rate, vocalSampleRate)
		}
		chunks = append(chunks, samples)
	}
	mixed := crossfadeConcat(chunks, vocalSampleRate)
	if len(mixed) == 0 {
		return nil, usedFallback, errors.New("vocalized segment produced no audio")
	}
	return pcmStreamFromSamples(ctx, mixed, vocalSampleRate), usedFallback, nil
}
