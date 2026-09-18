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

// EnableVocals is kept for phrase rotation on engines that opt into vocal
// tags. Recorded WAVs are not mixed with TTS — they do not match the mouth.
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
	// Recorded clips do not match the live TTS voice, so they are never
	// spliced. Stage tags are stripped (or rewritten only if the engine
	// actually renders them).
	return p.synthesizeWithFallbackObserved(ctx, sentence, stickFallback)
}

// prepareSpeechForTTS is the text one provider should hear: its vocal tags,
// then its join mode.
//
// A provider that re-splits sentence punctuation inside one call gets the
// group's short beats merged textually (joinShortBeats); one that carries
// prosody across the request gets the sentences as written. The rewrite lives
// here so it follows the engine that actually speaks, fallback included, and
// never reaches the transcript.
func prepareSpeechForTTS(sentence string, provider tts.Provider, vocals *vocalBank) string {
	spoken := trimSpeechResidue(renderVocalTagsFor(sentence, provider, vocals))
	if tts.JoinsSentencesOf(provider) {
		return spoken
	}
	return joinShortBeats(spoken)
}

// trimSpeechResidue drops the orphan punctuation a rendered-away tag leaves
// behind. "[laugh]. The build finished." strips to ". The build finished." on
// an engine with no vocal tags, and that leading period is read aloud as "dot"
// — or, once beats merge, turned into a comma. A lone "..." is a pause the
// engine should keep, so only single-character orphans go. Only runs when such
// a token is actually there, so ordinary text keeps its whitespace.
func trimSpeechResidue(s string) string {
	const residue = ".!?;:,"
	orphan := func(field string) bool {
		return len(field) == 1 && strings.Trim(field, residue) == ""
	}
	fields := strings.Fields(s)
	found := false
	for _, field := range fields {
		if orphan(field) {
			found = true
			break
		}
	}
	if !found {
		return s
	}
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if orphan(field) {
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(kept, " ")
}

// renderVocalTagsFor resolves [tag] markup for one engine.
//
//   - Default: stage tags are stripped (CustomVoice reads them as words).
//   - SupportsVocalTags + VocalTagRewriter: the engine's own mapping.
//   - SupportsVocalTags only: tags are passed through unchanged.
func renderVocalTagsFor(sentence string, provider tts.Provider, vocals *vocalBank) string {
	if !hasVocalTag(sentence) {
		return sentence
	}
	if !tts.RendersVocalTags(provider) {
		return stripVocalTags(sentence)
	}
	if rewriter, ok := provider.(tts.VocalTagRewriter); ok {
		var pick func(kind string) string
		if vocals != nil {
			pick = vocals.nextPhrase
		}
		return rewriter.RewriteVocalTags(sentence, pick)
	}
	return sentence
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
		text := strings.TrimSpace(stripVocalTags(part.text))
		if text == "" {
			continue
		}
		stream, fb, partObservation, err := p.synthesizeWithFallbackObserved(ctx, text, stickFallback)
		if errors.Is(err, errNothingToSpeak) {
			continue
		}
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
