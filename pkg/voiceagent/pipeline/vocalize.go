package pipeline

import (
	"regexp"
	"strings"
	"unicode"
)

// vocalKind is a non-speech sound the voice can play instead of reading a word.
// Qwen3-TTS has no breath/moan token — sending "ahh" makes it say the letters.
// These tags are the product seam: the model writes [moan], the pipeline plays
// a sound, the transcript never shows the markup.
const (
	vocalMoan    = "moan"
	vocalGasp    = "gasp"
	vocalLaugh   = "laugh"
	vocalSigh    = "sigh"
	vocalWhimper = "whimper"
	vocalKiss    = "kiss"
)

var vocalAlias = map[string]string{
	"moan":     vocalMoan,
	"moans":    vocalMoan,
	"ahh":      vocalMoan,
	"ahhs":     vocalMoan,
	"mmm":      vocalMoan,
	"gasp":     vocalGasp,
	"gasps":    vocalGasp,
	"laugh":    vocalLaugh,
	"laughs":   vocalLaugh,
	"haha":     vocalLaugh,
	"sigh":     vocalSigh,
	"sighs":    vocalSigh,
	"whimper":  vocalWhimper,
	"whimpers": vocalWhimper,
	"kiss":     vocalKiss,
	"kisses":   vocalKiss,
}

// vocalTagRE matches [moan], [gasp], [laugh], … including common aliases.
var vocalTagRE = regexp.MustCompile(`(?i)\[(moans?|gasps?|laughs?|sighs?|whimpers?|kisses?|ahhs?|mmm|haha)\]`)

type vocalPart struct {
	sound string // empty = speech
	text  string
}

func hasVocalTag(s string) bool {
	return vocalTagRE.MatchString(s)
}

func canonicalVocal(raw string) string {
	return vocalAlias[strings.ToLower(strings.TrimSpace(raw))]
}

// stripVocalTags removes sound markup from text bound for the transcript.
func stripVocalTags(s string) string {
	if s == "" || !hasVocalTag(s) {
		return s
	}
	s = vocalTagRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// splitVocalizations cuts a reply into speech and sound parts, in order.
// Speech keeps its words; tags become sound parts. Empty speech is dropped.
func splitVocalizations(s string) []vocalPart {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	locs := vocalTagRE.FindAllStringSubmatchIndex(s, -1)
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
		kind := canonicalVocal(s[loc[2]:loc[3]])
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

func vocalSpeechOnly(s string) string {
	return stripVocalTags(s)
}
