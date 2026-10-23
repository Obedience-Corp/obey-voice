package textclean

import (
	"regexp"
	"strings"
)

// StageTagRE matches the bracketed stage directions accepted by voice output.
var StageTagRE = regexp.MustCompile(`(?i)\[[A-Za-z][^\[\]]{0,40}\]`)

// StripStageDirections removes voice markup when evaluating or displaying the
// spoken words. Integrity checks and TTS use the same markup rules.
func StripStageDirections(s string) string {
	if s == "" || !StageTagRE.MatchString(s) {
		return s
	}
	s = StageTagRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}
