package pipeline

import (
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// vocalKind is a non-speech beat in a reply. The model writes [moan].
// Engines opt in with SupportsVocalTags. Qwen rewrites only tags that have
// a working codec spelling (laugh, sigh, hmm, ahh, mmm, huff). Moan/whimper/
// gasp/kiss are stripped: "Nnngh"/"Ahhn" come out as a spoken syllable, not
// a moan. Kokoro and Audio8 omit the flag, so every tag is stripped.
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
