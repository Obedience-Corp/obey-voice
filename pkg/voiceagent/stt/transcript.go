package stt

import (
	"regexp"
	"strings"
	"unicode"
)

// nonSpeechTag matches Whisper sound-event annotations — [BLANK_AUDIO], [Music],
// (applause), *buzzer* — which label background noise, not spoken words.
//
// For bracket/paren tags the closer is optional so a truncated partial like
// "[Buzz" (no ']') still strips. Streaming finals sometimes emit the open half
// of a tag before the model commits to a close; without this, that fragment
// becomes a turn. Asterisk forms still require a closing '*' so ordinary speech
// like "multiply * 2" is not swallowed.
var nonSpeechTag = regexp.MustCompile(`[\[(][^\])]*([\])]|$)|\*[^*]*\*`)

// stripNonSpeechTags removes those annotations. A transcript that was nothing
// but annotations collapses to "", which callers treat as silence.
func stripNonSpeechTags(text string) string {
	return nonSpeechTag.ReplaceAllString(text, " ")
}

// ghostTranscripts are isolated phrases Parakeet and Whisper emit on
// non-speech: echo tails after TTS, clipped VAD fragments, laptop-fan
// latches. Doctor names the "yeah" case (nlms over-gating). An isolated
// token is dropped; "yeah do that" stays a turn.
var ghostTranscripts = map[string]struct{}{
	"yeah": {}, "yea": {}, "yup": {}, "yep": {},
	"you": {}, "the": {}, "a": {}, "i": {},
	"uh": {}, "um": {}, "ah": {},
	"mm": {}, "hmm": {}, "mhm": {}, "mmhmm": {}, "mm-hmm": {},
	"uhhuh": {}, "uh-huh": {},
	"thank you": {}, "thanks for watching": {}, "thanks for listening": {},
	"you know": {},
}

// isGhostTranscript reports a whole-utterance STT hallucination, not a real
// reply. Punctuation is ignored so "Yeah." matches.
func isGhostTranscript(text string) bool {
	key := ghostKey(text)
	if key == "" {
		return false
	}
	_, ok := ghostTranscripts[key]
	return ok
}

func ghostKey(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
