package tts

import (
	"regexp"
	"strings"
)

// Vocal kinds used in [sigh]/[laugh] markup and recorded sound banks.
const (
	VocalMoan    = "moan"
	VocalGasp    = "gasp"
	VocalLaugh   = "laugh"
	VocalSigh    = "sigh"
	VocalWhimper = "whimper"
	VocalKiss    = "kiss"
	VocalHmm     = "hmm"
	VocalAhh     = "ahh"
	VocalMmm     = "mmm"
	VocalHuff    = "huff"
)

var vocalAlias = map[string]string{
	"moan":     VocalMoan,
	"moans":    VocalMoan,
	"gasp":     VocalGasp,
	"gasps":    VocalGasp,
	"laugh":    VocalLaugh,
	"laughs":   VocalLaugh,
	"haha":     VocalLaugh,
	"sigh":     VocalSigh,
	"sighs":    VocalSigh,
	"whimper":  VocalWhimper,
	"whimpers": VocalWhimper,
	"kiss":     VocalKiss,
	"kisses":   VocalKiss,
	"hmm":      VocalHmm,
	"hmms":     VocalHmm,
	"ahh":      VocalAhh,
	"ahhs":     VocalAhh,
	"relief":   VocalAhh,
	"mmm":      VocalMmm,
	"huff":     VocalHuff,
	"ugh":      VocalHuff,
}

// VocalTagRE matches known sound-bank kinds. Used to splice persona WAVs.
var VocalTagRE = regexp.MustCompile(`(?i)\[(moans?|gasps?|laughs?|sighs?|whimpers?|kiss(?:es)?|ahhs?|mmm|haha|hmms?|relief|huff|ugh)\]`)

// StageTagRE matches LLM stage directions, including ones we never listed
// ([mouth], [clears throat]). CustomVoice speaks these as words if they leak.
var StageTagRE = regexp.MustCompile(`(?i)\[[A-Za-z][^\[\]]{0,40}\]`)

// OnomatopoeiaPhrases are spellings Qwen3-TTS actually renders as a sound
// (aspirated onomatopoeia, leading h for breath). Empty means the tag is
// recognized so it can be stripped — do not send it to TTS.
//
// Moan/whimper/gasp/kiss have no working spelling: "Nnngh"/"Ahhn" come out as
// a spoken syllable (often "ming"), not a moan. Laugh and sigh are the ones
// that land as sounds in English CustomVoice.
var OnomatopoeiaPhrases = map[string][]string{
	VocalLaugh: {"Haha!", "Haha haha."},
	VocalSigh:  {"Hahh...", "Hah..."},
	VocalHmm:   {"Hmmm..."},
	VocalAhh:   {"Haaa..."},
	VocalMmm:   {"Hmmm..."},
	VocalHuff:  {"Huff."},
	// VocalMoan, VocalWhimper, VocalGasp, VocalKiss: no phrase — strip.
}

// VocalTagRewriter converts [sigh]/[laugh] markup into the form this engine
// synthesizes. Optional. pick, when non-nil, chooses among variants of a
// kind. Engines that omit this interface and advertise SupportsVocalTags
// receive the tags as written; otherwise tags are stripped.
type VocalTagRewriter interface {
	RewriteVocalTags(s string, pick func(kind string) string) string
}

// HasVocalTag reports whether s contains a stage-direction tag, known or not.
func HasVocalTag(s string) bool {
	return StageTagRE.MatchString(s)
}

// CanonicalVocal maps a tag body (sigh, sighs, haha, …) to a vocal kind.
func CanonicalVocal(raw string) string {
	return vocalAlias[strings.ToLower(strings.TrimSpace(raw))]
}

// StripVocalTags removes sound markup from text bound for the transcript or
// for an engine that cannot vocalize the tags.
func StripVocalTags(s string) string {
	if s == "" || !StageTagRE.MatchString(s) {
		return s
	}
	s = StageTagRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// ExpandOnomatopoeiaVocalTags rewrites supported tags into Qwen codec
// spellings. Unsupported kinds (moan, whimper, gasp, kiss) become nothing.
// pick chooses a variant; nil uses the first.
func ExpandOnomatopoeiaVocalTags(s string, pick func(kind string) string) string {
	if s == "" || !HasVocalTag(s) {
		return s
	}
	if pick == nil {
		pick = firstOnomatopoeiaPhrase
	}
	s = VocalTagRE.ReplaceAllStringFunc(s, func(tag string) string {
		m := VocalTagRE.FindStringSubmatch(tag)
		if len(m) < 2 {
			return ""
		}
		kind := CanonicalVocal(m[1])
		phrase := pick(kind)
		if phrase == "" {
			return ""
		}
		return phrase
	})
	return StripVocalTags(strings.TrimSpace(s))
}

func firstOnomatopoeiaPhrase(kind string) string {
	phrases := OnomatopoeiaPhrases[kind]
	if len(phrases) == 0 {
		return ""
	}
	return phrases[0]
}
