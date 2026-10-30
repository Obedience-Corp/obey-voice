package brain

import (
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

var imageResultNoun = regexp.MustCompile(`(?i)\b(?:images?|pictures?|photos?|png|jpe?g|gifs?)\b`)
var imageResultClaim = regexp.MustCompile(`(?i)\b(?:i(?: have|'ve|’ve)?|we(?: have|'ve|’ve)?) (?:attached|included|uploaded|sent)\b|\b(?:they(?: are|'re|’re)|it(?: is|'s|’s)|(?:the |these |those )?(?:images?|pictures?|photos?|files?)(?: are| is)?) attached\b`)
var imageResultPromise = regexp.MustCompile(`(?i)\bi(?: will|'ll|’ll| am going to) (?:get|find|fetch|download|attach|send|share|pull)\b`)
var imageResultQuotation = regexp.MustCompile("`[^`\\n]*`|\"[^\"\\n]*\"|“[^”\\n]*”")
var imageResultEarlier = regexp.MustCompile(`(?i)\b(?:earlier|above|previously|already|last (?:reply|message))\b`)

// Only guard explicit image delivery claims/promises on the held live-router
// response and its post-chat answer. The current request grounds pronouns such
// as "attached them" without treating unrelated attachment replies as images.
// This is not an intent router: the model must still select the tool
// and effort tier, and normal admission decides whether that work can run.
// Existing image replies are evidence for follow-ups about earlier attachments.
func (o *OllamaBrain) unsupportedImageResult(text, request string) bool {
	if imageResultEarlier.MatchString(text) {
		for _, turn := range o.canonical {
			if (turn.Role == "assistant" || turn.Role == "samantha") && len(turn.Attachments) > 0 {
				return false
			}
		}
	}
	text = chatmedia.ProtectImageExamples(text)
	if chatmedia.ReplyProse(text) != strings.TrimSpace(text) {
		return false // Explicit references are validated by the existing importer.
	}
	var prose []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced && !strings.HasPrefix(line, ">") {
			prose = append(prose, line)
		}
	}
	text = imageResultQuotation.ReplaceAllString(strings.Join(prose, "\n"), "")
	return (imageResultNoun.MatchString(text) || imageResultNoun.MatchString(request)) && (imageResultClaim.MatchString(text) || imageResultPromise.MatchString(text))
}
