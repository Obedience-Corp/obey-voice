package pipeline

import (
	"regexp"
	"strings"
	"unicode"
)

// vocalKind is a non-speech beat in a reply. The model writes [moan]; TTS
// hears the onomatopoeia Qwen actually renders as a human sound in the
// current throat (clone or preset). Square-bracket markup never reaches
// the synthesiser and never reaches the transcript.
//
// Official Qwen3-TTS has no <laugh> token. It does keep paralinguistics in
// the codec, so "Haha!", "Haaaah…", "Hahh…" come out as a laugh / moan /
// sigh in that speaker — the same trick the 1.7B instruct path uses.
const vocalSampleRate = 24000

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

// vocalPhrases are what Qwen3-TTS renders as the sound, in the active voice.
// Leading "h" adds breath. These are not English words to be read out —
// they are the onomatopoeia the model was trained to vocalize.
var vocalPhrases = map[string][]string{
	vocalMoan:    {"Haaaah...", "Nnngh...", "Ahhn..."},
	vocalGasp:    {"Hah!", "Haah!"},
	vocalLaugh:   {"Haha!", "Haha haha."},
	vocalSigh:    {"Hahh...", "Haaah."},
	vocalWhimper: {"Nngh...", "Mmh..."},
	vocalKiss:    {"Mwah."},
}

func firstVocalPhrase(kind string) string {
	phrases := vocalPhrases[kind]
	if len(phrases) == 0 {
		return ""
	}
	return phrases[0]
}

// expandVocalTags rewrites [moan] / [laugh] / … into the onomatopoeia the
// synthesiser should speak, so one TTS call produces the sound in the same
// throat as the surrounding words. pick chooses a variant; nil uses the first.
func expandVocalTags(s string, pick func(kind string) string) string {
	if s == "" || !hasVocalTag(s) {
		return s
	}
	if pick == nil {
		pick = firstVocalPhrase
	}
	return strings.TrimSpace(vocalTagRE.ReplaceAllStringFunc(s, func(tag string) string {
		m := vocalTagRE.FindStringSubmatch(tag)
		if len(m) < 2 {
			return ""
		}
		kind := canonicalVocal(m[1])
		phrase := pick(kind)
		if phrase == "" {
			return ""
		}
		return phrase
	}))
}
