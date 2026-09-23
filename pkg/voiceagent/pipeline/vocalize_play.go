package pipeline

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
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

func (p *Pipeline) synthUtteranceObserved(ctx context.Context, sentence string, stickFallback *fallbackLatch, joinBeats bool) (*audio.PCMStream, bool, synthesisObservation, error) {
	// Recorded clips do not match the live TTS voice, so they are never
	// spliced. Stage tags are stripped (or rewritten only if the engine
	// actually renders them).
	return p.synthesizeWithFallbackObserved(ctx, sentence, stickFallback, joinBeats)
}

// prepareSpeechForTTS is the text one provider should hear: its vocal tags,
// then, for a group the turn loop assembled around a held beat, its join mode.
//
// joinBeats is the loop's decision, not a property of the text: only a group
// it built from a short beat plus the sentence that beat belongs to may have
// its punctuation rewritten (joinShortBeats), and then only on a provider that
// re-splits sentences inside one call. Everything else — a whole text-mode
// reply, a status announcement, a filler, narration, fallback text — is spoken
// as written. Rewriting every string a non-joining engine speaks turned a
// 600-character reply into run-on speech.
//
// The rewrite lives here so it follows the engine that actually speaks,
// fallback included, and never reaches the transcript.
func prepareSpeechForTTS(sentence string, provider tts.Provider, vocals *vocalBank, joinBeats bool) string {
	spoken := trimSpeechResidue(renderVocalTagsFor(sentence, provider, vocals))
	if !joinBeats || tts.JoinsSentencesOf(provider) {
		return spoken
	}
	return joinShortBeats(spoken)
}

// trimSpeechResidue drops the orphan punctuation a rendered-away tag leaves at
// the front of an utterance: ". The build finished." is read aloud as "dot".
// Only leading tokens go. Punctuation further in is the speaker's — a spaced
// " , " is a pause and a trailing "?" is the sentence's type, which the engine
// reads as a contour, so dropping those turned every spaced question into a
// statement. Orphans a tag left mid-utterance are removed at the strip site
// instead (stripSpokenVocalTags), which knows the tag was there.
func trimSpeechResidue(s string) string {
	rest := strings.TrimLeftFunc(s, unicode.IsSpace)
	if !leadingSpeechOrphan(rest) {
		return s
	}
	for leadingSpeechOrphan(rest) {
		rest = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
	}
	return rest
}

// leadingSpeechOrphan reports a one-character punctuation token at the front of
// s. "..." is a pause, not an orphan.
func leadingSpeechOrphan(s string) bool {
	if s == "" || !strings.ContainsAny(s[:1], ".!?;:,") {
		return false
	}
	return len(s) == 1 || unicode.IsSpace(rune(s[1]))
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
		return stripSpokenVocalTags(sentence)
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
