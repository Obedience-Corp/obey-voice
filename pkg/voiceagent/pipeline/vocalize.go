package pipeline

import (
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// vocalKind is a non-speech beat in a reply. The model writes [moan].
// Default TTS strips stage tags. Recorded clips are not spliced — they
// do not match the live TTS voice. Engines opt in with SupportsVocalTags
// if they can render tags.
const vocalSampleRate = 24000

const (
	vocalMoan    = tts.VocalMoan
	vocalGasp    = tts.VocalGasp
	vocalLaugh   = tts.VocalLaugh
	vocalSigh    = tts.VocalSigh
	vocalWhimper = tts.VocalWhimper
	vocalKiss    = tts.VocalKiss
	vocalAhh     = tts.VocalAhh
)

var vocalPhrases = tts.OnomatopoeiaPhrases

type vocalPart struct {
	sound string // empty = speech
	text  string
}

func hasVocalTag(s string) bool { return tts.HasVocalTag(s) }

func stripVocalTags(s string) string { return tts.StripVocalTags(s) }

// stripSpokenVocalTags removes tags an engine cannot vocalize along with the
// punctuation glued to them: "[laugh]. The build finished." must not leave a
// bare "." for the engine to read as "dot". The tag's position is the only
// cheap proof that the period was the tag's and not a sentence's, so this is
// where it goes — trimSpeechResidue downstream can only judge the front of the
// utterance. A run of punctuation ("[sigh]...") is a pause and is kept.
func stripSpokenVocalTags(s string) string {
	spans := tts.VocalTagRE.FindAllStringIndex(s, -1)
	if len(spans) == 0 {
		return stripVocalTags(s)
	}
	var b strings.Builder
	last := 0
	for _, span := range spans {
		b.WriteString(s[last:span[0]])
		b.WriteString(" ")
		end := span[1]
		if end < len(s) && strings.ContainsAny(s[end:end+1], ".!?;:,") &&
			(end+1 == len(s) || !strings.ContainsAny(s[end+1:end+2], ".!?;:,")) {
			end++
		}
		last = end
	}
	b.WriteString(s[last:])
	return strings.Join(strings.Fields(b.String()), " ")
}

func expandVocalTags(s string, pick func(kind string) string) string {
	return tts.ExpandOnomatopoeiaVocalTags(s, pick)
}

// splitVocalizations cuts a reply into speech and sound parts, in order.
// Speech keeps its words; tags become sound parts. Empty speech is dropped.
func splitVocalizations(s string) []vocalPart {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	locs := tts.VocalTagRE.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return []vocalPart{{text: s}}
	}
	var parts []vocalPart
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			if text := trimSpeech(s[last:loc[0]]); text != "" {
				parts = append(parts, vocalPart{text: text})
			}
		}
		kind := tts.CanonicalVocal(s[loc[2]:loc[3]])
		if kind != "" {
			parts = append(parts, vocalPart{sound: kind})
		}
		last = loc[1]
	}
	if last < len(s) {
		if text := trimSpeech(s[last:]); text != "" {
			parts = append(parts, vocalPart{text: text})
		}
	}
	return parts
}

func trimSpeech(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';' || r == '-' || r == '—'
	})
	return strings.TrimSpace(s)
}

func vocalPartsHaveSound(parts []vocalPart) bool {
	for _, p := range parts {
		if p.sound != "" {
			return true
		}
	}
	return false
}
