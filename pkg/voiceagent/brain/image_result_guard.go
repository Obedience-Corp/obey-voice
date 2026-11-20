package brain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

var imageResultNoun = regexp.MustCompile(`(?i)\b(?:images?|pictures?|photos?|png|jpe?g|gifs?)\b`)
var imageResultClaim = regexp.MustCompile(`(?i)\b(?:i(?: have|'ve|’ve)?|we(?: have|'ve|’ve)?) (?:attached|included|uploaded|sent)\b|\b(?:they(?: are|'re|’re)|it(?: is|'s|’s)|(?:the |these |those )?(?:images?|pictures?|photos?|files?)(?: are| is)?) attached\b`)
var imageResultPromise = regexp.MustCompile(`(?i)\bi(?: will|'ll|’ll| am going to) (?:get|find|fetch|download|attach|send|share|pull)\b`)
var imageResultQuotation = regexp.MustCompile("`[^`\\n]*`|\"[^\"\\n]*\"|“[^”\\n]*”")
var imageResultEarlier = regexp.MustCompile(`(?i)\b(?:earlier|above|previously|already|last (?:reply|message))\b`)

// Guard explicit image delivery claims/promises on a held response. The current request grounds pronouns such
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

var errUnattachedImage = errors.New("the model claimed image delivery without a file reference")
var errMissingImageReference = errors.New("the reply contained no standalone image reference")

// Require a file for explicit delivery requests. Mentions such as "how do I
// share a picture?" and "did you put an image in the chat?" remain discussion.
const imageRequestStart = `(?i)(?:^|[.!?,]\s*)\s*(?:now )?(?:(?:can|could|would|will) you )?(?:please )?`

var requestPicture = regexp.MustCompile(imageRequestStart + `(?:show me|send me|attach|put|post|share) (?:an? |the |some |another )?(?:images?|photos?|pictures?|pngs?|jpe?gs?|gifs?)\b`)
var requestPictureInChat = regexp.MustCompile(imageRequestStart + `(?:put|send|post|attach|share)\b[^.!?\n]{0,80}\b(?:in|to) (?:the|this) chat\b`)
var imageFollowup = regexp.MustCompile(`(?i)\b(?:it|them|one|those|that)\b`)

// An immediate follow-up such as "find one online and put it in the chat"
// can refer to the image in the last exchange. Stop at the preceding user.
func (o *OllamaBrain) directImageRequest(input string, opts StreamOptions) string {
	if !opts.ToolsEnabled || opts.effectiveRouterPolicy() == RouterToolsRouteOnly || opts.OmitTurnInstruction || opts.Background || opts.AgentContinuation || opts.Discussion != nil {
		return ""
	}
	if imageResultNoun.MatchString(input) {
		return input
	}
	if !imageFollowup.MatchString(input) {
		return ""
	}
	for i := len(o.canonical) - 2; i >= 0; i-- {
		turn := o.canonical[i]
		if turn.Role != "user" && turn.Role != "assistant" && turn.Role != "samantha" || len(turn.ToolCalls) != 0 {
			continue
		}
		if imageResultNoun.MatchString(turn.Content) || len(turn.Attachments) != 0 {
			return "image"
		}
		if turn.Role == "user" {
			break
		}
	}
	return ""
}

func requestsPictureDelivery(input string) bool {
	return requestPicture.MatchString(input) || requestPictureInChat.MatchString(input)
}

func (o *OllamaBrain) imageReplyError(text, input, directRequest string) error {
	if directRequest != "" {
		protected := chatmedia.ProtectImageExamples(text)
		if chatmedia.ReplyProse(protected) != strings.TrimSpace(protected) {
			return chatmedia.DefaultStore().CheckReplyFiles(protected, ReplyWorkDir(o))
		}
		if requestsPictureDelivery(input) {
			return errMissingImageReference
		}
		input = directRequest
	}
	if o.unsupportedImageResult(text, input) {
		return errUnattachedImage
	}
	return nil
}

func (o *OllamaBrain) imageRepairPrompt(input, directRequest string, issue error) string {
	if directRequest == "" {
		return systemPrompt(o.cfg, "image-result-repair", nil)
	}
	return systemPrompt(o.cfg, "worker-image-repair", map[string]string{
		"brief": input, "work_dir": o.workDir, "import_error": fmt.Sprintf("%.512s", issue.Error()),
	})
}
