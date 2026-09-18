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
)

var vocalAlias = map[string]string{
	"moan":     VocalMoan,
	"moans":    VocalMoan,
	"ahh":      VocalMoan,
	"ahhs":     VocalMoan,
	"mmm":      VocalMoan,
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
}

// VocalTagRE matches [moan], [gasp], [laugh], … including common aliases.
var VocalTagRE = regexp.MustCompile(`(?i)\[(moans?|gasps?|laughs?|sighs?|whimpers?|kiss(?:es)?|ahhs?|mmm|haha)\]`)

// OnomatopoeiaPhrases are what Qwen3-TTS renders as the sound in the active
// voice. Leading "h" adds breath. These are not English words to be read out.
var OnomatopoeiaPhrases = map[string][]string{
	VocalMoan:    {"Haaaah...", "Nnngh...", "Ahhn..."},
	VocalGasp:    {"Hah!", "Haah!"},
	VocalLaugh:   {"Haha!", "Haha haha."},
	VocalSigh:    {"Hahh...", "Haaah."},
	VocalWhimper: {"Nngh...", "Mmh..."},
	// kiss is recognized so [kiss] can be stripped, not spoken.
}

// VocalTagRewriter converts [sigh]/[laugh] markup into the form this engine
// synthesizes. Optional. pick, when non-nil, chooses among variants of a
// kind (pipeline passes the vocal bank's rotator). Qwen rewrites to
// onomatopoeia. An engine that understands the tags natively can return s
// unchanged, or map them to its own syntax, and may ignore pick. Providers
// that omit this interface and advertise SupportsVocalTags receive the tags
// as written.
type VocalTagRewriter interface {
	RewriteVocalTags(s string, pick func(kind string) string) string
}

// HasVocalTag reports whether s contains a recognized sound tag.
func HasVocalTag(s string) bool {
	return VocalTagRE.MatchString(s)
}

// CanonicalVocal maps a tag body (sigh, sighs, haha, …) to a vocal kind.
func CanonicalVocal(raw string) string {
	return vocalAlias[strings.ToLower(strings.TrimSpace(raw))]
}

// StripVocalTags removes sound markup from text bound for the transcript or
// for an engine that cannot vocalize the tags.
func StripVocalTags(s string) string {
	if s == "" || !HasVocalTag(s) {
		return s
	}
	s = VocalTagRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// ExpandOnomatopoeiaVocalTags rewrites [moan]/[laugh]/… into the Qwen codec
// spellings. pick chooses a variant; nil uses the first. Engines that need a
// different mapping implement VocalTagRewriter instead of calling this.
func ExpandOnomatopoeiaVocalTags(s string, pick func(kind string) string) string {
	if s == "" || !HasVocalTag(s) {
		return s
	}
	if pick == nil {
		pick = firstOnomatopoeiaPhrase
	}
	return strings.TrimSpace(VocalTagRE.ReplaceAllStringFunc(s, func(tag string) string {
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
	}))
}

func firstOnomatopoeiaPhrase(kind string) string {
	phrases := OnomatopoeiaPhrases[kind]
	if len(phrases) == 0 {
		return ""
	}
	return phrases[0]
}
